package heights

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"common/chain"
	"common/chainoracle/blocks"
	"common/chainoracle/blocks/nmclient"
	"common/nodemanager"

	"devshard/chainoracle/blocks/direct"
	"devshard/chainoracle/blocks/failover"
	"devshard/chainoracle/blocks/tipcache"
	"devshard/cmd/gateway/config"
	"devshard/cmd/gateway/internal/logkey"
	"devshard/logging"
)

// OracleSources are the places a follower can read mainnet from; every one is optional.
type OracleSources struct {
	NodeManagerAddr string
	CometRPC        string
	Chain           *chain.Client
}

// Oracle is the gateway's own reading of mainnet. See README.md, "What it owns".
type Oracle struct {
	blocks.BlockOracle

	closeOnce sync.Once
	closers   []func()
}

// NewOracle returns nil when the follower is off or has nothing to follow.
func NewOracle(settings config.HeightSync, sources OracleSources) (*Oracle, error) {
	if !settings.Enabled || !settings.ChainOracle {
		return nil, nil
	}
	nodeManager, closeNodeManager, err := nodeManagerOracle(sources.NodeManagerAddr)
	if err != nil {
		return nil, err
	}
	var chainOracle blocks.BlockOracle
	if sources.Chain != nil {
		chainOracle = direct.NewFromChain(sources.Chain)
	}
	cometRPC := strings.TrimSpace(sources.CometRPC)
	if nodeManager == nil && chainOracle == nil && cometRPC == "" {
		if closeNodeManager != nil {
			closeNodeManager()
		}
		return nil, nil
	}

	cache := tipcache.New(failover.CometMaxAge)
	reading := failover.New(cache, nodeManager, chainOracle)
	oracle := &Oracle{BlockOracle: reading}
	if closeNodeManager != nil {
		oracle.closers = append(oracle.closers, closeNodeManager)
	}
	if cometRPC != "" {
		feedCtx, stopFeed := context.WithCancel(context.Background())
		oracle.closers = append(oracle.closers, stopFeed)
		if err := tipcache.StartCometWithHooks(feedCtx, cometRPC, cache, tipcache.CometHooks{
			OnConnected:    func() { reading.SetCometConnected(true) },
			OnDisconnected: func() { reading.SetCometConnected(false) },
		}); err != nil {
			logging.Warn("the height follower has no block feed",
				logkey.Subsystem, "heightsync", logkey.Error, err)
		}
	}
	logging.Info("height follower enabled", logkey.Subsystem, "heightsync",
		logkey.NodeManager, nodeManager != nil, logkey.DirectChain, chainOracle != nil, logkey.CometRPC, cometRPC != "")
	return oracle, nil
}

func (o *Oracle) Close() error {
	if o == nil {
		return nil
	}
	o.closeOnce.Do(func() {
		for _, closeFeed := range o.closers {
			closeFeed()
		}
	})
	return nil
}

func nodeManagerOracle(addr string) (blocks.BlockOracle, func(), error) {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return nil, nil, nil
	}
	client, err := nodemanager.NewClient(addr)
	if err != nil {
		return nil, nil, fmt.Errorf("height follower node-manager dial: %w", err)
	}
	lookup, err := nmclient.New(client.NodeManagerClient())
	if err != nil {
		_ = client.Close()
		return nil, nil, err
	}
	return lookup, func() { _ = client.Close() }, nil
}
