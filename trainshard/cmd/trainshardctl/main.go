package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"slices"
	"strings"

	"trainshard/internal/application/coord/assembly"
	"trainshard/internal/application/coord/ops"
	"trainshard/internal/domain/shard"
	"trainshard/internal/domain/shared"
	"trainshard/internal/domain/shared/vo"
	"trainshard/internal/infrastructure/adapters/chain"
	clockadapter "trainshard/internal/infrastructure/adapters/clock"
	"trainshard/internal/infrastructure/adapters/hosts"
	"trainshard/internal/infrastructure/adapters/signing/cosmos"
	"trainshard/internal/utils/clix"
)

var version = "dev"

func main() {
	if err := drive(); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		// a refusal is named by its code, the same way a per-node fault is, or the caller is left
		// with prose where the contract promised a code
		if code := shared.CodeOf(err); code != shared.CodeInternal {
			fmt.Fprintf(os.Stderr, "%s: %s\n", code, err)
		} else {
			fmt.Fprintln(os.Stderr, err)
		}
		os.Exit(1)
	}
}

func drive() error {
	command, args := "", os.Args[1:]
	if len(args) > 0 {
		command, args = args[0], args[1:]
	}

	switch {
	case slices.Contains([]string{"--version", "-version"}, command):
		fmt.Println(version)
		return nil
	case command == "", command == "help", clix.Asked(command):
		fmt.Print(usage())
		return nil
	}

	commands := catalog()
	run, found := commands[command]
	if !found {
		return fmt.Errorf("unknown command %q\n\n%s", command, usage())
	}
	if slices.ContainsFunc(args, clix.Asked) {
		return run(context.Background(), args)
	}

	cfg, err := load()
	if err != nil {
		return err
	}

	clock := clockadapter.System{}
	signer, err := key(cfg)
	if err != nil {
		return err
	}
	outside, err := connect(cfg, signer)
	if err != nil {
		return err
	}
	defer outside.close()

	hosts := hosts.New(&http.Client{}, cfg.directory, signer, clock, cfg.timeout)

	assembly.New(assembly.Config{Poll: cfg.pollInterval, Settle: cfg.settleWindow}, assembly.Deps{
		Chain:     outside.chain,
		Hosts:     hosts,
		Verifier:  signer,
		Submitter: outside.submitter,
		Lifecycle: outside.lifecycle,
		Clock:     clock,
	}, os.Stdout).Register(commands)
	ops.New(ops.Config{Timeout: cfg.timeout}, ops.Deps{
		Chain:   outside.chain,
		Hosts:   hosts,
		Streams: hosts,
		Reports: hosts,
		Clock:   clock,
	}, os.Stdout, os.Stdin).Register(commands)

	return commands[command](context.Background(), args)
}

type keys interface {
	Address() vo.Address
	Sign(payload []byte) []byte
	Recover(payload, signature []byte) (vo.Address, error)
}

func key(cfg config) (keys, error) {
	if cfg.privateKey != "" {
		return cosmos.FromHex(cfg.privateKey)
	}
	return cosmos.FromKeyring(cfg.keyringDir, cfg.keyringBackend, cfg.keyringPassword, cfg.keyName)
}

type outside struct {
	chain     shard.ChainReader
	submitter shard.ChainSubmitter
	lifecycle shard.ChainLifecycle
	close     func() error
}

func connect(cfg config, signer keys) (outside, error) {
	client, err := chain.Dial(chain.Config{Address: cfg.chainGRPC})
	if err != nil {
		return outside{}, err
	}
	account, signs := signer.(chain.Key)
	if !signs {
		return outside{}, fmt.Errorf("the chain only takes what a key signs")
	}
	creator := chain.NewSigner(client, account, cfg.chainID)
	return outside{chain: client, submitter: creator, lifecycle: creator, close: client.Close}, nil
}

func catalog() map[string]func(context.Context, []string) error {
	commands := map[string]func(context.Context, []string) error{}
	assembly.New(assembly.Config{}, assembly.Deps{}, io.Discard).Register(commands)
	ops.New(ops.Config{}, ops.Deps{}, io.Discard, nil).Register(commands)
	return commands
}

func usage() string {
	return "trainshardctl drives one training run.\n\n" +
		"  trainshardctl <command> <shard> [flags]\n" +
		"  trainshardctl <command> --help\n\n" +
		"commands: " + strings.Join(slices.Sorted(maps.Keys(catalog())), " ") + "\n"
}
