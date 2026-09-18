package chain

import (
	"strings"
	"testing"

	"github.com/productscience/inference/x/inference/types"

	"trainshard/internal/domain/shared/vo"
)

// The chain and the daemon each hold a copy of the endpoint grammar: the chain refuses what it
// will not store, the daemon refuses what it will not publish, and the coordinator parses what the
// chain stored. The two have to agree, or an address the chain accepted fails the shard read
func TestTheChainAndTheDaemonAgreeOnWhatAnEndpointIs(t *testing.T) {
	vectors := []string{
		"https://gpu.example",
		"https://gpu.example/trainshard-node2",
		"http://10.0.0.12:9700",
		"http://[fd00::12]:9700",
		"HTTPS://gpu.example",
		"gpu.example:9700",
		"ftp://gpu.example",
		"https://",
		"https://user@gpu.example",
		"https://gpu.example/",
		"https://gpu.example/t/",
		"https://gpu.example?x=1",
		"https://gpu.example?",
		"https://gpu.example#x",
		"https://gpu.example#",
		"http://10.0.0.12:",
		"http://10.0.0.12:/t",
		"http://10.0.0.12:0",
		"http://10.0.0.12:65535",
		"http://10.0.0.12:65536",
		"http://10.0.0.12:abc",
		"https://" + strings.Repeat("a", 300) + ".example",
		"",
		" https://gpu.example",
		"https://gpu.example ",
	}
	for _, raw := range vectors {
		t.Run(raw, func(t *testing.T) {
			_, daemonErr := vo.ParseEndpoint(raw)
			// an empty endpoint is a withdrawal on the chain and "unset" on the daemon; both sides
			// take it without a grammar check, the way MsgRefreshTrainingNodeOptIn.ValidateBasic does
			var chainErr error
			if raw != "" {
				chainErr = types.ValidateTrainingEndpoint(raw)
			}

			if (daemonErr == nil) != (chainErr == nil) {
				t.Fatalf("daemon says %v, chain says %v: the two grammars have drifted", daemonErr, chainErr)
			}
		})
	}
}
