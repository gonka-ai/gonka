package cmd

import (
	"fmt"
	"github.com/productscience/inference/internal/rpc"
	"github.com/spf13/cobra"
	"net/url"
	"os"
	"regexp"
)

func SetSeedCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "set-seeds [config-file-path] [node-rpc-url] [node-p2p-url]",
		Short: "Set seeds to the node address. RIGHT NOW ONLY SUPPORTS SINGLE NODE ADDRESS!",
		Args:  cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			configFilePath := args[0]
			nodeRpcUrl := args[1]
			nodeP2PUrl := args[2]

			err := setSeeds(configFilePath, nodeRpcUrl, nodeP2PUrl)
			if err != nil {
				return fmt.Errorf("Failed to set seed: %w", err)
			}

			fmt.Printf("Successfully set the seed to %s", nodeRpcUrl)
			return nil
		},
	}
	return cmd
}

func setSeeds(configFilePath string, nodeRpcUrl string, nodeP2PUrl string) error {
	p2pHostAndPort, err := parseURL(nodeP2PUrl)
	if err != nil {
		return fmt.Errorf("failed to parse seed URL: %w", err)
	}

	nodeId, err := rpc.GetNodeId(nodeRpcUrl)
	if err != nil {
		return fmt.Errorf("failed to get node id: %w", err)
	}

	fmt.Printf("Performed status request to seed node. Node id: %s\n", nodeId)

	seedString := fmt.Sprintf("%s@%s:%s", nodeId, p2pHostAndPort.Host, p2pHostAndPort.Port)

	fmt.Printf("Seed string = %s\n", seedString)

	fmt.Printf("Updating config. configFilePaht = %s\n", configFilePath)
	if err := updateSeeds(seedString, configFilePath); err != nil {
		return fmt.Errorf("failed to update config with the a new seed string: %w", err)
	}

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

// Config path: /root/.inference/
func updateSeeds(seeString, configPath string) error {
	// Read the entire config file into memory
	data, err := os.ReadFile(configPath)
	if err != nil {
		return fmt.Errorf("error reading file %s: %w", configPath, err)
	}

	// Compile a regex that looks for a line starting with:
	// seeds = "anything"
	// Using (?m) to enable multiline matching of ^ and $
	re := regexp.MustCompile(`(?m)^seeds\s*=\s*".*"$`)

	// Replace the entire line with the new seeds value
	replaced := re.ReplaceAllString(string(data), fmt.Sprintf(`seeds = "%s"`, seeString))

	// Write the updated content back to the file
	err = os.WriteFile(configPath, []byte(replaced), 0644)
	if err != nil {
		return fmt.Errorf("error writing file %s: %w", configPath, err)
	}

	return nil
}
