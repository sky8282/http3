package http3

import (
	"errors"
	"io"
)

const bodyCopyBufferSize int = 8 * 1024

type stream struct {
	str     io.ReadWriteCloser
	buf     []byte
	readLen int
}

func (obj *stream) Write(b []byte) (int, error) {
	obj.buf = obj.buf[:0]
	obj.buf = (&dataFrame{Length: uint64(len(b))}).Append(obj.buf)
	if _, err := obj.str.Write(obj.buf); err != nil {
		return 0, err
	}
	return obj.str.Write(b)
}
func (obj *stream) Close() error {
	return obj.str.Close()
}
func (obj *stream) Read(p []byte) (n int, err error) {
	if obj.readLen == 0 {
		data, err := obj.parseNextFrame()
		if err != nil {
			return 0, err
		}
		if frame, ok := data.(*dataFrame); !ok {
			return 0, errors.New("not data Frames")
		} else {
			obj.readLen = int(frame.Length)
		}
		if obj.readLen == 0 {
			obj.Close()
			return 0, io.EOF
		}
	}
	if len(p) > obj.readLen {
		n, err = obj.str.Read(p[:obj.readLen])
	} else {
		n, err = obj.str.Read(p)
	}
	obj.readLen -= n
	return
}
