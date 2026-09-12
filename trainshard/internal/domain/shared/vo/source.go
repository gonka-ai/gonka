package vo

import (
	"fmt"
	"net"
	"strconv"
	"strings"

	"trainshard/internal/domain/shared"
)

type Source struct {
	Host string
	Port int
}

func ParseSource(s string) (Source, error) {
	host, port, err := net.SplitHostPort(strings.TrimSpace(s))
	if err != nil {
		return Source{}, fmt.Errorf("source %q wants host:port: %w", s, shared.ErrValidation)
	}

	number, err := strconv.Atoi(port)
	if host == "" || err != nil || number < 1 || number > 65535 {
		return Source{}, fmt.Errorf("source %q: %w", s, shared.ErrValidation)
	}
	return Source{Host: host, Port: number}, nil
}

func (s Source) String() string { return net.JoinHostPort(s.Host, strconv.Itoa(s.Port)) }
