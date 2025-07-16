package client

import (
	"github.com/nats-io/nats.go"
	"strconv"
	"time"
)

func ConnectToNats(host string, port int, name string) (*nats.Conn, error) {
	return nats.Connect(
		"nats://"+host+":"+strconv.Itoa(port),
		nats.Name(name),
		nats.MaxReconnects(-1),
		nats.ReconnectWait(2*time.Second),
	)
}
