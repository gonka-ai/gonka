package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"trainshard/internal/domain/shared/vo"
	"trainshard/internal/infrastructure/adapters/hosts"
)

type config struct {
	actor     vo.Address
	secret    []byte
	chainSeed string
	directory hosts.Directory
	timeout   time.Duration

	pollInterval time.Duration
}

func load() (config, error) {
	cfg := config{
		actor:        vo.Address(env("ACTOR", "")),
		secret:       []byte(env("SHARED_SECRET", "")),
		chainSeed:    env("CHAIN_SEED", ""),
		timeout:      time.Minute,
		pollInterval: 10 * time.Second,
	}

	directory, err := loadDirectory(env("HOSTS", ""))
	if err != nil {
		return config{}, err
	}
	cfg.directory = directory

	switch {
	case cfg.actor == "":
		return config{}, fmt.Errorf("TRAINSHARDCTL_ACTOR is required, it is the address the shard was created from")
	case len(cfg.secret) == 0:
		return config{}, fmt.Errorf("TRAINSHARDCTL_SHARED_SECRET is required")
	case len(cfg.directory) == 0:
		return config{}, fmt.Errorf("TRAINSHARDCTL_HOSTS is required, it is a json file of participant to host url")
	case cfg.chainSeed == "":
		return config{}, fmt.Errorf("TRAINSHARDCTL_CHAIN_SEED is required until the chain client lands")
	}
	return cfg, nil
}

func loadDirectory(path string) (hosts.Directory, error) {
	if path == "" {
		return nil, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("hosts file: %w", err)
	}

	urls := map[string]string{}
	if err := json.Unmarshal(raw, &urls); err != nil {
		return nil, fmt.Errorf("hosts file %q: %w", path, err)
	}

	directory := make(hosts.Directory, len(urls))
	for participant, url := range urls {
		address, err := vo.ParseAddress(participant)
		if err != nil {
			return nil, err
		}
		directory[vo.Participant(address)] = strings.TrimSuffix(url, "/")
	}
	return directory, nil
}

func env(name, fallback string) string {
	if value, found := os.LookupEnv("TRAINSHARDCTL_" + name); found {
		return value
	}
	return fallback
}
