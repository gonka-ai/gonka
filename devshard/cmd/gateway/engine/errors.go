package engine

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"devshard/transport"
)

const connectChatPath = "/chat/completions"

// Errors the engine returns. See README.md, "Errors the engine returns".
var (
	ErrEmptyStream      = errors.New("empty content stream")
	ErrWinnerIncomplete = errors.New("winner failed after streaming started")
	ErrAnswerTruncated  = errors.New("host stopped before the answer finished")
)

// HostApplicationError is an upstream refusal that is the response; the API writes its message under HTTPStatus. See README.md, "Errors the engine returns".
type HostApplicationError struct {
	Code    string
	Type    string
	Message string
	Payload string
}

func (e *HostApplicationError) Error() string {
	if e == nil {
		return "host application error"
	}
	if e.Message != "" {
		return e.Message
	}
	if e.Payload != "" {
		return e.Payload
	}
	return "host application error"
}

func (e *HostApplicationError) HTTPStatus() int {
	if e == nil {
		return http.StatusBadGateway
	}
	if code, err := strconv.Atoi(e.Code); err == nil && isClientOrServerStatus(code) {
		return code
	}
	if strings.Contains(strings.ToLower(e.Type), "badrequest") {
		return http.StatusBadRequest
	}
	return http.StatusBadGateway
}

// UpstreamStatus reports the HTTP status a host's refusal stands for, whether it came over HTTP or Connect.
func UpstreamStatus(err error) (int, bool) {
	if upstream, refused := hostRefusal(err); refused {
		return upstream.StatusCode, true
	}
	return 0, false
}

func hostRefusal(err error) (*transport.UpstreamStatusError, bool) {
	var upstream *transport.UpstreamStatusError
	if errors.As(err, &upstream) && upstream != nil {
		return upstream, true
	}
	if status, devshardError, message, refused := transport.ConnectApplicationStatus(err); refused {
		return &transport.UpstreamStatusError{Path: connectChatPath, StatusCode: status, Body: message, DevshardError: devshardError}, true
	}
	return nil, false
}

func StatusForError(err error) int {
	if err == nil {
		return http.StatusOK
	}
	var hostErr *HostApplicationError
	if errors.As(err, &hostErr) {
		return hostErr.HTTPStatus()
	}
	if errors.Is(err, ErrHostsUnavailable) {
		return http.StatusServiceUnavailable
	}
	if status, ok := UpstreamStatus(err); ok && isThrottleStatus(status) {
		return http.StatusTooManyRequests
	}
	return http.StatusBadGateway
}

func isThrottleStatus(status int) bool {
	return status == http.StatusTooManyRequests || status == http.StatusServiceUnavailable
}

func isClientOrServerStatus(status int) bool {
	return status >= 400 && status <= 599
}
