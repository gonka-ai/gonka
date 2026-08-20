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
	chainfake "trainshard/internal/infrastructure/adapters/chain/fake"
	clockadapter "trainshard/internal/infrastructure/adapters/clock"
	"trainshard/internal/infrastructure/adapters/hosts"
	"trainshard/internal/infrastructure/adapters/signing/hmac"
	"trainshard/internal/utils/clix"
)

var version = "dev"

func main() {
	if err := drive(); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		fmt.Fprintln(os.Stderr, err)
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
	signer := hmac.New(cfg.secret, cfg.actor)
	chain, err := chainfake.Load(cfg.chainSeed, clock)
	if err != nil {
		return err
	}
	hosts := hosts.New(&http.Client{}, cfg.directory, signer, clock, cfg.timeout)

	assembly.New(assembly.Config{Poll: cfg.pollInterval, Settle: cfg.settleWindow}, assembly.Deps{
		Chain:     chain,
		Hosts:     hosts,
		Verifier:  signer,
		Submitter: chain,
		Clock:     clock,
	}, os.Stdout).Register(commands)
	ops.New(ops.Config{Timeout: cfg.timeout}, ops.Deps{
		Chain:   chain,
		Hosts:   hosts,
		Streams: hosts,
		Reports: hosts,
		Clock:   clock,
	}, os.Stdout, os.Stdin).Register(commands)

	return commands[command](context.Background(), args)
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
