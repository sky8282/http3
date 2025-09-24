package http3

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/sky8282/tools"
	"github.com/quic-go/qpack"
	"golang.org/x/net/http/httpguts"
	"golang.org/x/net/http2/hpack"
)

func (obj *Client) writeRequestHeader(str *stream, req *http.Request) error {
	return obj.writeHeaders(str, req)
}

func (obj *Client) writeHeaders(str *stream, req *http.Request) error {
	defer obj.encoder.Close()
	defer obj.headerBuf.Reset()
	if err := obj.encodeHeaders(req, "", actualContentLength(req)); err != nil {
		return err
	}
	b := make([]byte, 0, 128)
	b = (&headersFrame{Length: uint64(obj.headerBuf.Len())}).Append(b)
	if _, err := str.str.Write(b); err != nil {
		return err
	}
	_, err := str.str.Write(obj.headerBuf.Bytes())
	return err
}
func (obj *Client) encodeHeaders(req *http.Request, trailers string, contentLength int64) error {
	host := req.Host
	if host == "" {
		host = req.URL.Host
	}
	host, err := httpguts.PunycodeHostPort(host)
	if err != nil {
		return err
	}
	// http.NewRequest sets this field to HTTP/1.1
	isExtendedConnect := req.Method == http.MethodConnect && req.Proto != "" && req.Proto != "HTTP/1.1"
	var path string
	if req.Method != http.MethodConnect || isExtendedConnect {
		path = req.URL.RequestURI()
		if !validPseudoPath(path) {
			path = strings.TrimPrefix(path, req.URL.Scheme+"://"+host)
		}
	}
	enumerateHeaders := func(f func(name, value string)) {
		// 8.1.2.3 Request Pseudo-Header Fields
		// The :path pseudo-header field includes the path and query parts of the
		// target URI (the path-absolute production and optionally a '?' character
		// followed by the query production (see Sections 3.3 and 3.4 of
		// [RFC3986]).
		f(":authority", host)
		f(":method", req.Method)
		if req.Method != http.MethodConnect || isExtendedConnect {
			f(":path", path)
			f(":scheme", req.URL.Scheme)
		}
		if isExtendedConnect {
			f(":protocol", req.Proto)
		}
		if trailers != "" {
			f("trailer", trailers)
		}

		var didUA bool
		for k, vv := range req.Header {
			if strings.EqualFold(k, "host") || strings.EqualFold(k, "content-length") {
				// Host is :authority, already sent.
				// Content-Length is automatic, set below.
				continue
			} else if strings.EqualFold(k, "connection") || strings.EqualFold(k, "proxy-connection") ||
				strings.EqualFold(k, "transfer-encoding") || strings.EqualFold(k, "upgrade") ||
				strings.EqualFold(k, "keep-alive") {
				// Per 8.1.2.2 Connection-Specific Header
				// Fields, don't send connection-specific
				// fields. We have already checked if any
				// are error-worthy so just ignore the rest.
				continue
			} else if strings.EqualFold(k, "user-agent") {
				// Match Go's http1 behavior: at most one
				// User-Agent. If set to nil or empty string,
				// then omit it. Otherwise if not mentioned,
				// include the default (below).
				didUA = true
				if len(vv) < 1 {
					continue
				}
				vv = vv[:1]
				if vv[0] == "" {
					continue
				}

			}

			for _, v := range vv {
				f(k, v)
			}
		}
		if shouldSendReqContentLength(req.Method, contentLength) {
			f("content-length", strconv.FormatInt(contentLength, 10))
		}
		if !didUA {
			f("user-agent", tools.UserAgent)
		}
	}
	// Do a first pass over the headers counting bytes to ensure
	// we don't exceed cc.peerMaxHeaderListSize. This is done as a
	// separate pass before encoding the headers to prevent
	// modifying the hpack state.
	hlSize := uint64(0)
	enumerateHeaders(func(name, value string) {
		hf := hpack.HeaderField{Name: name, Value: value}
		hlSize += uint64(hf.Size())
	})
	enumerateHeaders(func(name, value string) {
		name = strings.ToLower(name)
		obj.encoder.WriteField(qpack.HeaderField{Name: name, Value: value})
	})
	return nil
}
func validPseudoPath(v string) bool {
	return (len(v) > 0 && v[0] == '/') || v == "*"
}
func actualContentLength(req *http.Request) int64 {
	if req.Body == nil {
		return 0
	}
	if req.ContentLength != 0 {
		return req.ContentLength
	}
	return -1
}

// shouldSendReqContentLength reports whether the http2.Transport should send
// a "content-length" request header. This logic is basically a copy of the net/http
// transferWriter.shouldSendContentLength.
// The contentLength is the corrected contentLength (so 0 means actually 0, not unknown).
// -1 means unknown.
func shouldSendReqContentLength(method string, contentLength int64) bool {
	if contentLength > 0 {
		return true
	}
	if contentLength < 0 {
		return false
	}
	// For zero bodies, whether we send a content-length depends on the method.
	// It also kinda doesn't matter for http2 either way, with END_STREAM.
	switch method {
	case "POST", "PUT", "PATCH":
		return true
	default:
		return false
	}
}
func responseFromHeaders(headerFields []qpack.HeaderField) (*http.Response, error) {
	hdr, err := parseHeaders(headerFields, false)
	if err != nil {
		return nil, err
	}
	if hdr.Status == "" {
		return nil, errors.New("missing status field")
	}
	rsp := &http.Response{
		Proto:         "HTTP/3.0",
		ProtoMajor:    3,
		Header:        hdr.Headers,
		ContentLength: hdr.ContentLength,
	}
	status, err := strconv.Atoi(hdr.Status)
	if err != nil {
		return nil, fmt.Errorf("invalid status code: %w", err)
	}
	rsp.StatusCode = status
	rsp.Status = hdr.Status + " " + http.StatusText(status)
	return rsp, nil
}

type header struct {
	// Pseudo header fields defined in RFC 9114
	Path      string
	Method    string
	Authority string
	Scheme    string
	Status    string
	// for Extended connect
	Protocol string
	// parsed and deduplicated
	ContentLength int64
	// all non-pseudo headers
	Headers http.Header
}

func parseHeaders(headers []qpack.HeaderField, isRequest bool) (header, error) {
	hdr := header{Headers: make(http.Header, len(headers))}
	for _, h := range headers {
		h.Name = strings.ToLower(h.Name)
		if h.IsPseudo() {
			var isResponsePseudoHeader bool // pseudo headers are either valid for requests or for responses
			switch h.Name {
			case ":path":
				hdr.Path = h.Value
			case ":method":
				hdr.Method = h.Value
			case ":authority":
				hdr.Authority = h.Value
			case ":protocol":
				hdr.Protocol = h.Value
			case ":scheme":
				hdr.Scheme = h.Value
			case ":status":
				hdr.Status = h.Value
				isResponsePseudoHeader = true
			default:
				return header{}, fmt.Errorf("unknown pseudo header: %s", h.Name)
			}
			if isRequest && isResponsePseudoHeader {
				return header{}, fmt.Errorf("invalid request pseudo header: %s", h.Name)
			}
			if !isRequest && !isResponsePseudoHeader {
				return header{}, fmt.Errorf("invalid response pseudo header: %s", h.Name)
			}
		} else {
			hdr.Headers.Add(h.Name, h.Value)
			if h.Name == "content-length" {
				cl, err := strconv.ParseInt(h.Value, 10, 64)
				if err != nil {
					return header{}, fmt.Errorf("invalid content length: %w", err)
				}
				hdr.ContentLength = cl
			}
		}
	}
	return hdr, nil
}

