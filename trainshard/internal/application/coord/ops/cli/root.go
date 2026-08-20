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
	"trainshard/internal/domain/shared/ports"
	"trainshard/internal/utils/clix"
)

type UseCases struct {
	Deploy    *usecases.DeployUseCase
	Start     *usecases.StartUseCase
	Stop      *usecases.StopUseCase
	Status    *usecases.StatusUseCase
	Report    *usecases.CollectReportUseCase
	Artifacts *usecases.CollectArtifactsUseCase
	Logs      *usecases.StreamLogsUseCase
	Shell     *usecases.OpenShellUseCase
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
	commands["artifacts"] = c.Artifacts
	commands["logs"] = c.Logs
	commands["shell"] = c.Shell
}

func (c *Commands) Deploy(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("deploy <shard> [flags] [-- command]", flag.ContinueOnError)
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
	rest, err := clix.Parse(flag.NewFlagSet("start <shard>", flag.ContinueOnError), args, "shard")
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
	flags := flag.NewFlagSet("stop <shard> [flags]", flag.ContinueOnError)
	grace := flags.Duration("grace", 30*time.Second, "how long a container may take to exit on its own")

	rest, err := clix.Parse(flags, args, "shard")
	if err != nil {
		return err
	}
	command, err := toRunCommand(rest, c.timeout, c.clock.Now())
	if err != nil {
		return err
	}

	results, err := c.uc.Stop.Execute(ctx, usecases.StopCommand{RunCommand: command, Grace: *grace})
	if err != nil {
		return err
	}
	return c.printResults(results)
}

func (c *Commands) Status(ctx context.Context, args []string) error {
	rest, err := clix.Parse(flag.NewFlagSet("status <shard>", flag.ContinueOnError), args, "shard")
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
	fmt.Fprintln(out, "NODE\tSTATE\tPREPARED\tMESH\tGPUS\tDISK\tQUOTA\tREASON")
	for _, node := range statuses {
		fmt.Fprintf(out, "%s\t%s\t%t\t%t\t%d\t%d\t%d\t%s\n",
			node.Node, node.State, node.Prepared, node.MeshUp, node.GPUsInUse, node.DiskBytes, node.DiskQuotaBytes, reason(node.Fault))
	}
	return out.Flush()
}

func (c *Commands) Report(ctx context.Context, args []string) error {
	rest, err := clix.Parse(flag.NewFlagSet("report <shard>", flag.ContinueOnError), args, "shard")
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
	for _, node := range reports {
		if len(node.Images) == 0 {
			fmt.Fprintf(out, "%s\t\t\t%s\t%s\n", node.Node, exit(node.ExitCode), reason(node.Fault))
			continue
		}
		for _, image := range node.Images {
			fmt.Fprintf(out, "%s\t%s\t%s\t%s\t%s\n",
				node.Node, image.Image, image.At.UTC().Format(time.RFC3339), exit(node.ExitCode), reason(node.Fault))
		}
	}
	return out.Flush()
}

func (c *Commands) Artifacts(ctx context.Context, args []string) error {
	rest, err := clix.Parse(flag.NewFlagSet("artifacts <shard> <participant/node>", flag.ContinueOnError), args, "shard", "node")
	if err != nil {
		return err
	}
	command, err := toNodeCommand(rest)
	if err != nil {
		return err
	}
	return c.uc.Artifacts.Execute(ctx, command, c.out)
}

func (c *Commands) Logs(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("logs <shard> <participant/node> [flags]", flag.ContinueOnError)
	tail := flags.Int("tail", 0, "how many lines to start from, newest first")

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
	rest, err := clix.Parse(flag.NewFlagSet("shell <shard> <participant/node>", flag.ContinueOnError), args, "shard", "node")
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
