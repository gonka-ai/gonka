package main

import (
	"errors"

	"devshard/transport"
)

const connectChatPath = "/chat/completions"

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
