package cmd

import (
	"fmt"
	"github.com/productscience/inference/internal/rpc"
	"github.com/spf13/cobra"
	"os"
	"regexp"
	"strconv"
)

func SetStateSync() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "set-statesync [config-file-path] [boolean-value]",
		Short: "Set state sync can enable or disable node syncing from snapshots. Look at set-statesync-rpc-servers command to enable syncing correctly.",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			configFilePath := args[0]
			val := args[1]

			enable, err := strconv.ParseBool(val)
			if err != nil {
				return fmt.Errorf("invalid value for statesync.enable: %s (expected 'true' or 'false')", val)
			}

			err = updateStateSync(configFilePath, enable)
			if err != nil {
				return fmt.Errorf("failed to set statesync.enable: %w", err)
			}

			fmt.Printf("Successfully set the statesync.enable to %s", val)
			return nil
		},
	}
	return cmd
}

func updateStateSync(configPath string, enable bool) error {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return fmt.Errorf("error reading file %s: %w", configPath, err)
	}

	// Compile a regex that looks for a line starting with:
	// enable = true || false
	// Using (?m) to enable multiline matching of ^ and $
	re := regexp.MustCompile(`(?m)^enable\s*=\s*(true|false)$`)

	replaced := re.ReplaceAllString(string(data), fmt.Sprintf("enable = %v", enable))
	if err = os.WriteFile(configPath, []byte(replaced), 0644); err != nil {
		return fmt.Errorf("error writing file %s: %w", configPath, err)
	}

	return nil
}

func SetRpcServers() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "set-statesync-rpc-servers [config-file-path] [rpc-server-1] [rpc-server-2]",
		Short: "Set state sync rpc servers sets 2 rpc servers from which node can get a snapshot",
		Args:  cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			configFilePath := args[0]
			nodeRpcUrl1 := args[1]
			nodeRpcUrl2 := args[2]

			err := updateRpcServers(configFilePath, nodeRpcUrl1, nodeRpcUrl2)
			if err != nil {
				return fmt.Errorf("failed to set statesync.rpc_servers: %w", err)
			}

			fmt.Printf("Successfully set the statesync.rpc_servers with values %v and %v", nodeRpcUrl1, nodeRpcUrl2)
			return nil
		},
	}
	return cmd
}

func updateRpcServers(configPath, rpcServer1, rpcServer2 string) error {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return fmt.Errorf("error reading file %s: %w", configPath, err)
	}

	// Compile a regex that looks for a line starting with:
	// rpc_servers = "some_value"
	// Using (?m) to enable multiline matching of ^ and $
	re := regexp.MustCompile(`(?m)^rpc_servers\s*=\s*".*"$`)

	replaced := re.ReplaceAllString(string(data), fmt.Sprintf(`rpc_servers = "%s,%s"`, rpcServer1, rpcServer2))
	if err = os.WriteFile(configPath, []byte(replaced), 0644); err != nil {
		return fmt.Errorf("error writing file %s: %w", configPath, err)
	}
	return nil
}

func SetTrustedBlock() *cobra.Command {
	cmd := &cobra.Command{
		Use: "set-statesync-trusted-block [config-file-path] [trusted-node-rpc-server] [trusted-block-period]",
		Short: "Set state sync trusted block sets 2 rpc servers from which node can get a snapshot. " +
			"Trusted block period argument is added for test purposes and used to calculate trusted_block_height as " +
			"latest_block_height - trusted_block_period = trusted_block_height",
		Args: cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			configFilePath := args[0]
			trustedNode := args[1]
			trustedBlockPeriodStr := args[2]

			trustedBlockPeriod, err := strconv.ParseUint(trustedBlockPeriodStr, 10, 64)
			if err != nil {
				return fmt.Errorf("invalid value for trusted-block-period %s:, %w", trustedBlockPeriodStr, err)
			}

			trustedBlockHeight, trustedBlockHash, err := rpc.GetTrustedBlock(trustedNode, trustedBlockPeriod)
			if err != nil {
				return fmt.Errorf("failed to fetch trusted block from node %v: %w", trustedNode, err)
			}

			if err = updateTrustedBlock(configFilePath, trustedBlockHeight, trustedBlockHash); err != nil {
				return fmt.Errorf("failed to set trusted block wih block_height %v and block_hash %v: %w", trustedBlockHeight, trustedBlockHash, err)
			}
			fmt.Printf("Successfully set the statesync.trust_height with value %v and statesync.trust_hash with value %v", trustedBlockHeight, trustedBlockHash)
			return nil
		},
	}
	return cmd
}

func updateTrustedBlock(configPath string, blockHeight uint64, blockHash string) error {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return fmt.Errorf("error reading file %s: %w", configPath, err)
	}

	// Compile a regex that looks for a line starting with:
	// trust_hash = "some_value" || some_value || ""
	// Using (?m) to enable multiline matching of ^ and $
	re := regexp.MustCompile(`(?m)^\s*trust_hash\s*=\s*"?[^"\n]*"?\s*$`)
	replaced := re.ReplaceAllString(string(data), fmt.Sprintf(`trust_hash = "%s"`, blockHash))

	// Compile a regex that looks for a line starting with:
	// trust_height = "some_value"|| some_value
	// Using (?m) to enable multiline matching of ^ and $
	re = regexp.MustCompile(`(?m)^trust_height\s*=\s*(?:"([^"]*)"|(\S+))$`)
	replaced = re.ReplaceAllString(replaced, fmt.Sprintf(`trust_height = %v`, blockHeight))

	if err = os.WriteFile(configPath, []byte(replaced), 0644); err != nil {
		return fmt.Errorf("error writing file %s: %w", configPath, err)
	}
	return nil
}
