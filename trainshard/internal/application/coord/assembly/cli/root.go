package cli

import (
	"context"
	"fmt"
	"io"
	"time"

	usecases "trainshard/internal/application/coord/assembly/use_cases"
	"trainshard/internal/domain/shard"
	"trainshard/internal/domain/shared/ports"
	"trainshard/internal/utils/clix"
)

type Commands struct {
	prepare   *usecases.PrepareMeshUseCase
	lifecycle shard.ChainLifecycle
	clock     ports.Clock
	out       io.Writer
}

func New(prepare *usecases.PrepareMeshUseCase, lifecycle shard.ChainLifecycle, clock ports.Clock, out io.Writer) *Commands {
	return &Commands{prepare: prepare, lifecycle: lifecycle, clock: clock, out: out}
}

func (c *Commands) Register(commands map[string]func(context.Context, []string) error) {
	commands["assemble"] = c.Assemble
	commands["prepare"] = c.Prepare
	commands["settle"] = c.Settle
}

func (c *Commands) Assemble(ctx context.Context, args []string) error {
	rest, err := clix.Parse(clix.Command("assemble <proposal>",
		"Turns a passed proposal into a shard: the chain reserves nodes of the proposal's gpu\nprofile. Prints the shard id the other commands take.",
		"trainshardctl assemble 3"), args, "proposal")
	if err != nil {
		return err
	}
	proposal, err := toProposalID(rest)
	if err != nil {
		return err
	}

	shardID, err := c.lifecycle.Assemble(ctx, proposal)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(c.out, "%s\n", shardID)
	return err
}

func (c *Commands) Settle(ctx context.Context, args []string) error {
	rest, err := clix.Parse(clix.Command("settle <shard>",
		"Closes the shard on the chain and hands its nodes back to their hosts. Stop the run\nand collect its report first.",
		"trainshardctl settle 1"), args, "shard")
	if err != nil {
		return err
	}
	shardID, err := toShardID(rest)
	if err != nil {
		return err
	}
	return c.lifecycle.Settle(ctx, shardID)
}

func (c *Commands) Prepare(ctx context.Context, args []string) error {
	flags := clix.Command("prepare <shard> [flags]",
		"Brings up the mesh between the nodes of the shard and waits until every node reaches\nits peers. A node that cannot join within -wait is released back to its host.",
		"trainshardctl prepare 1", "trainshardctl prepare 1 -wait 10m")
	wait := flags.Duration("wait", 30*time.Minute, "how long a node has to report its mesh identity and reach its peers before it is released")

	rest, err := clix.Parse(flags, args, "shard")
	if err != nil {
		return err
	}
	shardID, err := toShardID(rest)
	if err != nil {
		return err
	}

	result, err := c.prepare.Execute(ctx, shardID, c.clock.Now().Add(*wait))
	if err != nil {
		return err
	}
	return c.print(result)
}

func (c *Commands) print(result usecases.PrepareResult) error {
	for _, released := range result.Released {
		if _, err := fmt.Fprintf(c.out, "released %s: %s\n", released.Node, released.Reason); err != nil {
			return err
		}
	}
	if len(result.Config.Peers) == 0 {
		return fmt.Errorf("mesh is not connected and no single node explains it: %v", result.Failed)
	}

	master, _ := result.Config.Master()
	_, err := fmt.Fprintf(c.out, "mesh of %d nodes, master %s\n", len(result.Config.Peers), master.Node)
	return err
}
