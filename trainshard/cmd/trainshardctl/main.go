package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"

	"trainshard/internal/application/coord/assembly"
	"trainshard/internal/application/coord/ops"
	chainfake "trainshard/internal/infrastructure/adapters/chain/fake"
	clockadapter "trainshard/internal/infrastructure/adapters/clock"
	"trainshard/internal/infrastructure/adapters/hosts"
	"trainshard/internal/infrastructure/adapters/signing/hmac"
)

func main() {
	if err := drive(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func drive() error {
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
	hosts := hosts.New(&http.Client{Timeout: cfg.timeout}, cfg.directory, signer, clock)

	commands := map[string]func(context.Context, []string) error{}
	assembly.New(assembly.Config{Poll: cfg.pollInterval}, assembly.Deps{
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

	if len(os.Args) < 2 {
		return fmt.Errorf("%s", usage(commands))
	}
	command, found := commands[os.Args[1]]
	if !found {
		return fmt.Errorf("unknown command %q\n\n%s", os.Args[1], usage(commands))
	}
	return command(context.Background(), os.Args[2:])
}

func usage(commands map[string]func(context.Context, []string) error) string {
	names := make([]string, 0, len(commands))
	for name := range commands {
		names = append(names, name)
	}
	sort.Strings(names)
	return "trainshardctl drives one training run:\n  " + strings.Join(names, " ")
}
