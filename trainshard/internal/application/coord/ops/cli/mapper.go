package cli

import (
	"flag"
	"fmt"
	"strconv"
	"strings"
	"time"

	usecases "trainshard/internal/application/coord/ops/use_cases"
	"trainshard/internal/domain/run"
	"trainshard/internal/domain/shared"
	"trainshard/internal/domain/shared/vo"
)

func toRunCommand(args []string, timeout time.Duration, now time.Time) (usecases.RunCommand, error) {
	if len(args) == 0 {
		return usecases.RunCommand{}, fmt.Errorf("shard: %w", shared.ErrValidation)
	}
	shardID, err := vo.ParseShardID(args[0])
	if err != nil {
		return usecases.RunCommand{}, err
	}

	return usecases.RunCommand{
		Shard:     shardID,
		RequestID: vo.NewRequestID(),
		Deadline:  now.Add(timeout),
	}, nil
}

func toNodeCommand(args []string) (usecases.NodeCommand, error) {
	if len(args) < 2 {
		return usecases.NodeCommand{}, fmt.Errorf("shard and node: %w", shared.ErrValidation)
	}
	shardID, err := vo.ParseShardID(args[0])
	if err != nil {
		return usecases.NodeCommand{}, err
	}

	participant, nodeID, found := strings.Cut(args[1], "/")
	if !found {
		return usecases.NodeCommand{}, fmt.Errorf("node must be participant/node_id: %w", shared.ErrValidation)
	}
	node, err := vo.ParseNodeRef(participant, nodeID)
	if err != nil {
		return usecases.NodeCommand{}, err
	}
	return usecases.NodeCommand{Shard: shardID, Node: node}, nil
}

func toRunSpec(image string, gpus int, disk int64, env envFlag, sources *sourceFlag, command []string) (run.RunSpec, error) {
	digest, err := vo.ParseImageDigest(image)
	if err != nil {
		return run.RunSpec{}, err
	}
	return run.RunSpec{
		Image:     digest,
		Command:   command,
		Env:       env,
		Sources:   *sources,
		Resources: run.Resources{GPUs: gpus, DiskBytes: disk},
	}, nil
}

func parse(flags *flag.FlagSet, args []string) ([]string, error) {
	if len(args) == 0 {
		return nil, fmt.Errorf("shard: %w", shared.ErrValidation)
	}
	if err := flags.Parse(args[1:]); err != nil {
		return nil, err
	}
	return args[:1], nil
}

func reason(fault *shared.Fault) string {
	if fault == nil {
		return ""
	}
	return fault.Code + ": " + fault.Reason
}

func exit(code *int) string {
	if code == nil {
		return ""
	}
	return strconv.Itoa(*code)
}

type envFlag map[string]string

func (e envFlag) String() string { return "" }

func (e envFlag) Set(value string) error {
	name, content, found := strings.Cut(value, "=")
	if !found || name == "" {
		return fmt.Errorf("env must be name=value: %w", shared.ErrValidation)
	}
	e[name] = content
	return nil
}

type sourceFlag []vo.Source

func (s *sourceFlag) String() string { return "" }

func (s *sourceFlag) Set(value string) error {
	source, err := vo.ParseSource(value)
	if err != nil {
		return err
	}
	*s = append(*s, source)
	return nil
}
