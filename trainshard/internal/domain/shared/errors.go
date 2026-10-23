package shared

import "errors"

var (
	ErrValidation   = errors.New("validation")
	ErrUnauthorized = errors.New("unauthorized")
	ErrForbidden    = errors.New("forbidden")
	ErrNotFound     = errors.New("not found")
	ErrConflict     = errors.New("conflict")
	ErrUnavailable  = errors.New("unavailable")
)

const CodeInternal = "INTERNAL"

type Error struct {
	Code string
	Kind error
	Msg  string
}

func New(code string, kind error, msg string) *Error {
	return &Error{Code: code, Kind: kind, Msg: msg}
}

func (e *Error) Error() string { return e.Msg }

func (e *Error) Unwrap() error { return e.Kind }

func CodeOf(err error) string {
	var coded *Error
	if errors.As(err, &coded) {
		return coded.Code
	}
	return CodeInternal
}

type Fault struct {
	Code   string
	Reason string
}

func NewFault(err error) *Fault {
	if err == nil {
		return nil
	}
	return &Fault{Code: CodeOf(err), Reason: err.Error()}
}
