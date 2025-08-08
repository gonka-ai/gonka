package client

import (
	"decentralized-api/internal/nats/server"
	"github.com/nats-io/nats.go"
	"strconv"
	"time"
)

func ConnectToNats(host string, port int, name string) (*nats.Conn, error) {
	if host == "" {
		host = server.DefaultHost
	}

	if port == 0 {
		port = server.DefaultPort
	}

	return nats.Connect(
		"nats://"+host+":"+strconv.Itoa(port),
		nats.Name(name),
		nats.MaxReconnects(-1),
		nats.ReconnectWait(2*time.Second),
	)
}
