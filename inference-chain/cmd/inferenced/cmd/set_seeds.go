package cmd

import (
	"encoding/json"
	"fmt"
	"github.com/spf13/cobra"
	"log"
	"net/http"
	"net/url"
	"os"
)

type statusResponse struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int    `json:"id"`
	Result  struct {
		NodeInfo nodeInfo `json:"node_info"`
	} `json:"result"`
}

type nodeInfo struct {
	ID string `json:"id"`
}

func SetSeedCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "set-seeds [node-host] [node-p2p-port]",
		Short: "Set seeds to the node address. RIGHT NOW ONLY SUPPORTS SINGLE NODE ADDRESS!",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			nodeRpcUrl := args[0]
			nodeP2PPort := args[1]

			err := setSeeds(nodeRpcUrl, nodeP2PPort)
			if err != nil {
				return fmt.Errorf("Failed to set seed: %w", err)
			}

			fmt.Printf("Successfully set the seed to %s", nodeRpcUrl)
			return nil
		},
	}
	return cmd
}

func setSeeds(nodeRpcUrl string, nodeP2PPort string) error {
	statusUrl := fmt.Sprintf("%s/status", nodeRpcUrl)

	resp, err := http.Get(statusUrl)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("received non-OK HTTP status: %s", resp.Status)
	}

	var genResp statusResponse
	if err := json.NewDecoder(resp.Body).Decode(&genResp); err != nil {
		return fmt.Errorf("failed to decode genesis JSON: %w", err)
	}

	fmt.Printf("Performed status request to seed node. Node id: %s\n", genResp.Result.NodeInfo.ID)

	seedHostAndPort, err := parseURL(nodeRpcUrl)
	if err != nil {
		return fmt.Errorf("failed to parse seed URL: %w", err)
	}

	seedString := fmt.Sprintf("%s@%s:%s", genResp.Result.NodeInfo.ID, seedHostAndPort.Host, nodeP2PPort)

	fmt.Printf("Seed string = %s", seedString)

	listCwd()

	return nil
}

type urlParseResult struct {
	Host string
	Port string
}

func parseURL(rawURL string) (*urlParseResult, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("could not parse URL: %w", err)
	}

	host := u.Hostname()
	port := u.Port()

	// If no port is provided, pick the default one based on the scheme
	if port == "" {
		switch u.Scheme {
		case "http":
			port = "80"
		case "https":
			port = "443"
		default:
			return nil, fmt.Errorf("unsupported scheme: %q", u.Scheme)
		}
	}

	return &urlParseResult{
		Host: host,
		Port: port,
	}, nil
}

func listCwd() {
	cwd, err := os.Getwd()
	if err != nil {
		log.Fatalf("Failed to get current working directory: %v", err)
	}
	fmt.Printf("Current Working Directory: %s\n", cwd)

	// List the contents of the current working directory
	contents, err := os.ReadDir(cwd)
	if err != nil {
		log.Fatalf("Failed to read directory contents: %v", err)
	}

	fmt.Println("Contents:")
	for _, entry := range contents {
		if entry.IsDir() {
			fmt.Printf("[DIR]  %s\n", entry.Name())
		} else {
			fmt.Printf("[FILE] %s\n", entry.Name())
		}
	}
}
