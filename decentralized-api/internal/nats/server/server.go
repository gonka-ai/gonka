package server

import (
	"decentralized-api/apiconfig"
	"decentralized-api/logging"
	"time"

	natssrv "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/pkg/errors"
	types2 "github.com/productscience/inference/x/inference/types"
)

const (
	TxsToSendStream      = "txs_to_send"
	TxsToObserveStream   = "txs_to_observe"
	TxsBatchStartStream  = "txs_batch_start"
	TxsBatchFinishStream = "txs_batch_finish"

	storageDir    = "/root/.dapi/.nats"
	defaultMaxAge = 24 * 60 * 60 // 24 hours

	DefaultPort = 4222
	DefaultHost = "0.0.0.0"
)

type NatsServer interface {
	Start() error
}

type server struct {
	conf apiconfig.NatsServerConfig
	ns   *natssrv.Server
}

func NewServer(config apiconfig.NatsServerConfig) NatsServer {
	return &server{
		conf: config,
	}
}

func (s *server) Start() error {
	if s.conf.Host == "" {
		s.conf.Host = DefaultHost
	}

	if s.conf.Port == 0 {
		s.conf.Port = DefaultPort
	}

	if s.conf.MaxMessagesAgeSeconds == 0 {
		s.conf.MaxMessagesAgeSeconds = defaultMaxAge
	}

	logging.Info("starting nats server", types2.Messages, "port", s.conf.Port, "host", s.conf.Host)

	opts := &natssrv.Options{
		Host:      s.conf.Host,
		Port:      s.conf.Port,
		JetStream: true,
		StoreDir:  storageDir,
	}

	ns, err := natssrv.NewServer(opts)
	if err != nil {
		return errors.Wrap(err, "failed to create NATS server")
	}

	s.ns = ns
	go ns.Start()

	for i := 0; i < 3; i++ {
		time.Sleep(1 * time.Second)
		if ns.ReadyForConnections(2 * time.Second) {
			break
		}
		if i == 2 {
			return errors.New("NATS server not ready after 3 attempts")
		}
	}

	return s.createJetStreamTopics([]string{TxsToSendStream, TxsToObserveStream, TxsBatchStartStream, TxsBatchFinishStream})
}

func (s *server) createJetStreamTopics(topicNames []string) error {
	nc, err := nats.Connect(s.ns.ClientURL())
	if err != nil {
		return errors.Wrap(err, "failed to connect to embedded NATS")
	}
	js, err := nc.JetStream()
	if err != nil {
		return errors.Wrap(err, "failed to get JetStream context")
	}

	for _, topic := range topicNames {
		_, err = js.AddStream(&nats.StreamConfig{
			Name:     topic,
			Subjects: []string{topic},
			MaxAge:   time.Duration(s.conf.MaxMessagesAgeSeconds) * time.Second,
			Storage:  nats.FileStorage,
		})

		if err != nil && !errors.Is(err, nats.ErrStreamNameAlreadyInUse) {
			return errors.Wrap(err, "failed to add stream for topic "+topic)
		}
	}
	return nil
}
