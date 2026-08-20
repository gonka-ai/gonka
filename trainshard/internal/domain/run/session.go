package run

import "io"

func Recorded(session io.ReadWriter, sink io.Writer) io.ReadWriter {
	return recorded{session: session, sink: sink}
}

type recorded struct {
	session io.ReadWriter
	sink    io.Writer
}

func (r recorded) Read(p []byte) (int, error) {
	n, err := r.session.Read(p)
	if n > 0 {
		if _, failed := r.sink.Write(p[:n]); failed != nil {
			return n, failed
		}
	}
	return n, err
}

func (r recorded) Write(p []byte) (int, error) {
	if _, err := r.sink.Write(p); err != nil {
		return 0, err
	}
	return r.session.Write(p)
}
