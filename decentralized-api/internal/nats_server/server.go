package nats_server

import (
	"decentralized-api/apiconfig"
	"decentralized-api/cosmosclient"
	"decentralized-api/logging"
	natssrv "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/pkg/errors"
	types2 "github.com/productscience/inference/x/inference/types"
	"time"
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
	logging.Info("starting nats server", types2.Messages,
		"port", s.conf.Port,
		"host", s.conf.Host,
		"test_mode", s.conf.TestMode,
		"storage_dir", s.conf.StorageDir,
	)

	opts := &natssrv.Options{
		Host:      s.conf.Host,
		Port:      s.conf.Port,
		JetStream: true,
	}

	if s.conf.TestMode {
		logging.Info("ignore storage dir, nats running in test mode", types2.Messages)
	} else {
		opts.StoreDir = s.conf.StorageDir
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

	return s.createJetStreamTopics([]string{cosmosclient.TxsToObserveTopic, cosmosclient.TxsToSendTopic})
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
			Storage:  nats.FileStorage,
		})

		if err != nil && !errors.Is(err, nats.ErrStreamNameAlreadyInUse) {
			return errors.Wrap(err, "failed to add stream for topic "+topic)
		}
	}
	return nil
}
