package http3

import (
	"errors"
	"io"
	"net/http"
)

func sendRequestBody(str *stream, body io.ReadCloser) error {
	defer body.Close()
	buf := make([]byte, bodyCopyBufferSize)
	_, err := io.CopyBuffer(str, body, buf)
	return err
}

func (obj *Client) sendRequest(req *http.Request, str *stream) error {
	defer str.Close()
	if err := obj.writeRequestHeader(str, req); err != nil {
		return err
	}
	if req.Body != nil {
		return sendRequestBody(str, req.Body)
	}
	return nil
}

func (obj *Client) readResponse(req *http.Request, str *stream) (*http.Response, error) {
	defer str.Close()
	frame, err := str.parseNextFrame()
	if err != nil {
		return nil, err
	}
	headFrame, ok := frame.(*headersFrame)
	if !ok {
		return nil, errors.New("not head Frames")
	}
	headerBlock := make([]byte, headFrame.Length)
	if _, err := io.ReadFull(str.str, headerBlock); err != nil {
		return nil, err
	}
	hfs, err := obj.decoder.DecodeFull(headerBlock)
	if err != nil {
		return nil, err
	}
	res, err := responseFromHeaders(hfs)
	if err != nil {
		return nil, err
	}
	res.Request = req
	res.Body = str
	return res, nil
}

func (obj *Client) doRequest(req *http.Request, str *stream) (*http.Response, error) {
	err := obj.sendRequest(req, str)
	if err != nil {
		return nil, err
	}
	return obj.readResponse(req, str)
}
