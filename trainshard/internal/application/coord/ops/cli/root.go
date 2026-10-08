package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"text/tabwriter"
	"time"

	usecases "trainshard/internal/application/coord/ops/use_cases"
	"trainshard/internal/domain/run"
	"trainshard/internal/domain/shared"
	"trainshard/internal/domain/shared/ports"
	"trainshard/internal/utils/clix"
)

type UseCases struct {
	Deploy *usecases.DeployUseCase
	Start  *usecases.StartUseCase
	Stop   *usecases.StopUseCase
	Status *usecases.StatusUseCase
	Report *usecases.CollectReportUseCase
	Logs   *usecases.StreamLogsUseCase
	Shell  *usecases.OpenShellUseCase
}

type Commands struct {
	uc      UseCases
	clock   ports.Clock
	timeout time.Duration
	out     io.Writer
	in      io.Reader
}

func New(uc UseCases, clock ports.Clock, timeout time.Duration, out io.Writer, in io.Reader) *Commands {
	return &Commands{uc: uc, clock: clock, timeout: timeout, out: out, in: in}
}

func (c *Commands) Register(commands map[string]func(context.Context, []string) error) {
	commands["deploy"] = c.Deploy
	commands["start"] = c.Start
	commands["stop"] = c.Stop
	commands["status"] = c.Status
	commands["report"] = c.Report
	commands["logs"] = c.Logs
	commands["shell"] = c.Shell
}

func (c *Commands) Deploy(ctx context.Context, args []string) error {
	flags := clix.Command("deploy <shard> [flags] [-- command]",
		"Places the run on every node of the shard: an image built on the proposal's base image,\nits gpus, disk and the outside addresses it may reach. What follows -- is the command\nthe container runs. Nothing runs until start, and a running run is refused: stop it first.",
		"trainshardctl deploy 1 -image registry.example.com/run@sha256:<digest> -gpus 1 \\\n      -disk-bytes 10737418240 -source s3.amazonaws.com:443 -env EPOCHS=3 -- python train.py")
	image := flags.String("image", "", "image digest to run, built on the proposal's base image")
	gpus := flags.Int("gpus", 0, "gpus per node")
	disk := flags.Int64("disk-bytes", 0, "disk quota per node")
	env := envFlag{}
	flags.Var(env, "env", "environment variable as name=value, repeatable")
	sources := &sourceFlag{}
	flags.Var(sources, "source", "outside address the run may reach, as host:port, repeatable")

	rest, err := clix.Parse(flags, args, "shard")
	if err != nil {
		return err
	}
	command, err := toRunCommand(rest, c.timeout, c.clock.Now())
	if err != nil {
		return err
	}
	run, err := toRunSpec(*image, *gpus, *disk, env, sources, flags.Args())
	if err != nil {
		return err
	}

	results, err := c.uc.Deploy.Execute(ctx, usecases.DeployCommand{RunCommand: command, Run: run})
	if err != nil {
		return err
	}
	return c.printResults(results)
}

func (c *Commands) Start(ctx context.Context, args []string) error {
	rest, err := clix.Parse(clix.Command("start <shard>",
		"Starts the deployed run on every node of the shard. Refused unless every node is\nprepared, on the mesh and holds the same image.",
		"trainshardctl start 1"), args, "shard")
	if err != nil {
		return err
	}
	command, err := toRunCommand(rest, c.timeout, c.clock.Now())
	if err != nil {
		return err
	}

	results, err := c.uc.Start.Execute(ctx, command)
	if err != nil {
		return err
	}
	return c.printResults(results)
}

func (c *Commands) Stop(ctx context.Context, args []string) error {
	flags := clix.Command("stop <shard> [flags]",
		"Stops the run on every node of the shard. The shard stays open until it is settled.",
		"trainshardctl stop 1", "trainshardctl stop 1 -grace 2m")
	grace := flags.Duration("grace", 30*time.Second, "how long a container may take to exit on its own")

	rest, err := clix.Parse(flags, args, "shard")
	if err != nil {
		return err
	}
	if *grace < 0 {
		return fmt.Errorf("grace must not be negative: %w", shared.ErrValidation)
	}
	command, err := toRunCommand(rest, c.timeout, c.clock.Now())
	if err != nil {
		return err
	}
	graceGiven := false
	flags.Visit(func(entry *flag.Flag) {
		if entry.Name == "grace" {
			graceGiven = true
		}
	})

	results, err := c.uc.Stop.Execute(ctx, usecases.StopCommand{RunCommand: command, Grace: *grace, GraceGiven: graceGiven})
	if err != nil {
		return err
	}
	return c.printResults(results)
}

func (c *Commands) Status(ctx context.Context, args []string) error {
	rest, err := clix.Parse(clix.Command("status <shard>",
		"Shows each node's container state, image and exit code, whether it is prepared and on\nthe mesh, the peers it has not heard from for 3 min, its gpu and disk use, and why a\nnode is not ready yet.",
		"trainshardctl status 1"), args, "shard")
	if err != nil {
		return err
	}
	command, err := toRunCommand(rest, c.timeout, c.clock.Now())
	if err != nil {
		return err
	}

	statuses, err := c.uc.Status.Execute(ctx, command)
	if err != nil {
		return err
	}

	out := tabwriter.NewWriter(c.out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(out, "NODE\tSTATE\tIMAGE\tEXIT\tPREPARED\tMESH\tNOT_HEARD\tGPUS\tDISK\tQUOTA\tREASON")
	silent := 0
	for _, node := range statuses {
		why := node.Waiting
		if node.Fault != nil {
			why = reason(node.Fault)
		}
		if node.Unanswered() {
			silent++
		}
		fmt.Fprintf(out, "%s\t%s\t%s\t%s\t%t\t%t\t%s\t%d\t%d\t%d\t%s\n",
			node.Node, node.State, node.Image, exit(node.ExitCode), node.Prepared, node.MeshUp, peers(node.MeshSilent), node.GPUsInUse, node.DiskBytes, node.DiskQuotaBytes, why)
	}
	if err := out.Flush(); err != nil {
		return err
	}
	return told(silent, len(statuses))
}

// a fault is still an answer; only a run where no node answered fails the exit code
func told(silent, asked int) error {
	if asked > 0 && silent == asked {
		return fmt.Errorf("none of %d nodes gave an answer, see the reasons above", asked)
	}
	return nil
}

func (c *Commands) Report(ctx context.Context, args []string) error {
	rest, err := clix.Parse(clix.Command("report <shard>",
		"Shows the images each node ran, when, and how the run exited. Collect it before the\nshard is settled: a settled shard no longer answers.",
		"trainshardctl report 1"), args, "shard")
	if err != nil {
		return err
	}
	command, err := toRunCommand(rest, c.timeout, c.clock.Now())
	if err != nil {
		return err
	}

	reports, err := c.uc.Report.Execute(ctx, command)
	if err != nil {
		return err
	}

	out := tabwriter.NewWriter(c.out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(out, "NODE\tIMAGE\tRAN AT\tEXIT\tREASON")
	silent := 0
	for _, node := range reports {
		if node.Unanswered() {
			silent++
		}
		if len(node.Images) == 0 {
			fmt.Fprintf(out, "%s\t\t\t%s\t%s\n", node.Node, exit(node.ExitCode), reason(node.Fault))
			continue
		}
		for _, image := range node.Images {
			fmt.Fprintf(out, "%s\t%s\t%s\t%s\t%s\n",
				node.Node, image.Image, image.At.UTC().Format(time.RFC3339), exit(node.ExitCode), reason(node.Fault))
		}
	}
	if err := out.Flush(); err != nil {
		return err
	}
	return told(silent, len(reports))
}

func (c *Commands) Logs(ctx context.Context, args []string) error {
	flags := clix.Command("logs <shard> <participant/node> [flags]",
		"Streams the run output of one node, named participant/node as status prints it.",
		"trainshardctl logs 1 gonka1s0acz7xxe2t6zz8ne5rm7eesu6tk7rhnv6u3gj/node1", "trainshardctl logs 1 gonka1s0acz7xxe2t6zz8ne5rm7eesu6tk7rhnv6u3gj/node1 -tail 100")
	tail := flags.Int("tail", 0, "start from the last this many lines instead of the whole output")

	rest, err := clix.Parse(flags, args, "shard", "node")
	if err != nil {
		return err
	}
	command, err := toNodeCommand(rest)
	if err != nil {
		return err
	}
	command.Tail = *tail

	return c.uc.Logs.Execute(ctx, command, c.out)
}

func (c *Commands) Shell(ctx context.Context, args []string) error {
	rest, err := clix.Parse(clix.Command("shell <shard> <participant/node>",
		"Opens an interactive shell in the run container of one node, named participant/node\nas status prints it. Exit the shell to close the session.",
		"trainshardctl shell 1 gonka1s0acz7xxe2t6zz8ne5rm7eesu6tk7rhnv6u3gj/node1"), args, "shard", "node")
	if err != nil {
		return err
	}
	command, err := toNodeCommand(rest)
	if err != nil {
		return err
	}
	return c.uc.Shell.Execute(ctx, command, console{in: c.in, out: c.out})
}

type console struct {
	in  io.Reader
	out io.Writer
}

func (c console) Read(p []byte) (int, error) { return c.in.Read(p) }

func (c console) Write(p []byte) (int, error) { return c.out.Write(p) }

func (c *Commands) printResults(results []run.NodeResult) error {
	out := tabwriter.NewWriter(c.out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(out, "NODE\tSTATE\tIMAGE\tREASON")

	failed := 0
	for _, result := range results {
		if !result.OK() {
			failed++
		}
		fmt.Fprintf(out, "%s\t%s\t%s\t%s\n", result.Node, result.State, result.Image, reason(result.Fault))
	}
	if err := out.Flush(); err != nil {
		return err
	}
	if failed > 0 {
		return fmt.Errorf("%d of %d nodes failed", failed, len(results))
	}
	return nil
}
