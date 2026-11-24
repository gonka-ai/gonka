package api

import (
	"errors"
	"net/http"
	"os"
	"time"

	"devshard/cmd/gateway/journal"
)

// BodyReadError is a request body that did not arrive whole: the client stalled, left or sent a broken stream.
type BodyReadError struct{ Cause error }

func (e *BodyReadError) Error() string {
	if errors.Is(e.Cause, os.ErrDeadlineExceeded) {
		return "request body was not received in time"
	}
	return "request body could not be read"
}

func (e *BodyReadError) Unwrap() error { return e.Cause }

func bodyReadFailure(w http.ResponseWriter, err error) error {
	if err == nil {
		return nil
	}
	// An expired read deadline stops the server's drain of the unread body from waiting on a client that has stopped sending.
	_ = http.NewResponseController(w).SetReadDeadline(time.Now())
	var oversized *http.MaxBytesError
	if errors.As(err, &oversized) || errors.Is(err, ErrRequestBodiesFull) {
		return err
	}
	w.Header().Set("Connection", "close")
	return &BodyReadError{Cause: err}
}

func (s *Server) refuseBody(w http.ResponseWriter, requestID string, err error) {
	var unread *BodyReadError
	if errors.As(err, &unread) {
		s.events.RequestBodyUnread(journal.RequestLine{RequestID: requestID, BodyErr: unread.Cause})
	}
	s.writeErrorFor(w, err)
}
