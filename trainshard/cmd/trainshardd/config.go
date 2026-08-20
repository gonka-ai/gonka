package main

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"trainshard/internal/domain/run"
	"trainshard/internal/domain/shared/vo"
)

type config struct {
	participant      vo.Participant
	nodes            []vo.NodeRef
	listen           string
	admin            string
	stateDir         string
	chainSeed        string
	machine          string
	dockerSocket     string
	sandboxImage     string
	volumeRoot       string
	volumeMount      string
	containerUser    string
	containerUID     int
	containerGID     int
	memoryBytes      int64
	nanoCPUs         int64
	nvidiaSMI        string
	meshEndpoint     string
	meshPortBase     int
	meshPorts        int
	meshKeyDir       string
	deniedCIDRs      []string
	secret           []byte
	inventory        vo.GPUInventory
	limits           run.Limits
	minFreeDiskBytes int64
	supportedVersion string
	logLevel         string
	logFormat        string

	stopGrace         time.Duration
	prepareDeadline   time.Duration
	reconcileInterval time.Duration
	optInTTL          time.Duration
	refreshInterval   time.Duration
	signatureWindow   time.Duration
	requestTTL        time.Duration
}

func load() (config, error) {
	cfg := config{
		participant:      vo.Participant(env("PARTICIPANT", "")),
		listen:           env("LISTEN", "127.0.0.1:9700"),
		admin:            env("ADMIN_LISTEN", ""),
		stateDir:         env("STATE_DIR", "/var/lib/trainshardd"),
		chainSeed:        env("CHAIN_SEED", ""),
		machine:          env("MACHINE", "memory"),
		dockerSocket:     env("DOCKER_SOCKET", "/var/run/docker.sock"),
		sandboxImage:     env("SANDBOX_IMAGE", "registry.k8s.io/pause:3.9"),
		containerUser:    env("CONTAINER_USER", "1000:1000"),
		nvidiaSMI:        env("NVIDIA_SMI", "nvidia-smi"),
		meshEndpoint:     env("MESH_ENDPOINT", ""),
		secret:           []byte(env("SHARED_SECRET", "")),
		supportedVersion: env("SUPPORTED_VERSION", ""),
		logLevel:         env("LOG_LEVEL", "info"),
		logFormat:        env("LOG_FORMAT", "text"),
	}

	gpus, err := number("GPUS", 8)
	if err != nil {
		return config{}, err
	}
	maxDisk, err := number("MAX_DISK_BYTES", 1<<40)
	if err != nil {
		return config{}, err
	}
	maxSources, err := number("MAX_SOURCES", 16)
	if err != nil {
		return config{}, err
	}
	minFree, err := number("MIN_FREE_DISK_BYTES", 1<<40)
	if err != nil {
		return config{}, err
	}

	cfg.inventory = vo.GPUInventory{Model: env("GPU_MODEL", "H100"), Count: int(gpus)}
	cfg.limits = run.Limits{MaxGPUs: int(gpus), MaxDiskBytes: maxDisk, MaxSources: int(maxSources)}
	cfg.minFreeDiskBytes = minFree
	cfg.deniedCIDRs = list(env("DENIED_CIDRS", ""))

	for name, target := range map[string]*int64{
		"CONTAINER_MEMORY_BYTES": &cfg.memoryBytes,
		"CONTAINER_NANO_CPUS":    &cfg.nanoCPUs,
	} {
		value, err := number(name, 0)
		if err != nil {
			return config{}, err
		}
		*target = value
	}

	cfg.containerUID, cfg.containerGID, err = account(cfg.containerUser)
	if err != nil {
		return config{}, err
	}

	cfg.meshPortBase, cfg.meshPorts, err = portRange(env("MESH_PORTS", "51820-51827"))
	if err != nil {
		return config{}, err
	}

	cfg.volumeRoot = env("VOLUME_ROOT", filepath.Join(cfg.stateDir, "volumes"))
	cfg.volumeMount = env("VOLUME_MOUNT", cfg.volumeRoot)
	cfg.meshKeyDir = env("MESH_KEY_DIR", filepath.Join(cfg.stateDir, "mesh"))

	for name, target := range map[string]*time.Duration{
		"STOP_GRACE":         &cfg.stopGrace,
		"PREPARE_DEADLINE":   &cfg.prepareDeadline,
		"RECONCILE_INTERVAL": &cfg.reconcileInterval,
		"OPT_IN_TTL":         &cfg.optInTTL,
		"REFRESH_INTERVAL":   &cfg.refreshInterval,
		"SIGNATURE_WINDOW":   &cfg.signatureWindow,
		"REQUEST_TTL":        &cfg.requestTTL,
	} {
		value, err := duration(name, defaults[name])
		if err != nil {
			return config{}, err
		}
		*target = value
	}

	cfg.nodes, err = nodeRefs(cfg.participant, env("NODES", ""))
	if err != nil {
		return config{}, err
	}
	return cfg, cfg.validate()
}

var defaults = map[string]time.Duration{
	"STOP_GRACE":         30 * time.Second,
	"PREPARE_DEADLINE":   30 * time.Minute,
	"RECONCILE_INTERVAL": 10 * time.Second,
	"OPT_IN_TTL":         15 * time.Minute,
	"REFRESH_INTERVAL":   5 * time.Minute,
	"SIGNATURE_WINDOW":   time.Minute,
	"REQUEST_TTL":        time.Hour,
}

func (c config) validate() error {
	switch {
	case c.participant == "":
		return fmt.Errorf("TRAINSHARD_PARTICIPANT is required")
	case len(c.nodes) == 0:
		return fmt.Errorf("TRAINSHARD_NODES is required")
	case len(c.secret) == 0:
		return fmt.Errorf("TRAINSHARD_SHARED_SECRET is required")
	case c.admin != "" && !loopback(c.admin):
		return fmt.Errorf("TRAINSHARD_ADMIN_LISTEN %q must be a loopback address, abort carries no signature and whoever reaches the port can stop a run", c.admin)

	case c.machine == "docker" && c.meshEndpoint == "":
		return fmt.Errorf("TRAINSHARD_MESH_ENDPOINT is required on a docker machine")
	case c.machine == "docker" && c.memoryBytes <= 0:
		return fmt.Errorf("TRAINSHARD_CONTAINER_MEMORY_BYTES is required on a docker machine, an unlimited run can take the host down")
	case c.machine == "docker" && c.nanoCPUs <= 0:
		return fmt.Errorf("TRAINSHARD_CONTAINER_NANO_CPUS is required on a docker machine, an unlimited run can starve the inference server")

	case len(c.nodes) > c.meshPorts:
		return fmt.Errorf("TRAINSHARD_MESH_PORTS covers %d ports and this host holds %d nodes", c.meshPorts, len(c.nodes))

	case c.refreshInterval >= c.optInTTL:
		return fmt.Errorf("TRAINSHARD_REFRESH_INTERVAL must be shorter than TRAINSHARD_OPT_IN_TTL")
	}
	return nil
}

func loopback(address string) bool {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func portRange(spec string) (int, int, error) {
	first, last, found := strings.Cut(spec, "-")
	base, err := strconv.Atoi(strings.TrimSpace(first))
	if err != nil {
		return 0, 0, fmt.Errorf("TRAINSHARD_MESH_PORTS %q must be first-last: %w", spec, err)
	}
	if !found {
		return base, 1, nil
	}

	end, err := strconv.Atoi(strings.TrimSpace(last))
	if err != nil {
		return 0, 0, fmt.Errorf("TRAINSHARD_MESH_PORTS %q must be first-last: %w", spec, err)
	}
	if end < base {
		return 0, 0, fmt.Errorf("TRAINSHARD_MESH_PORTS %q ends before it begins", spec)
	}
	return base, end - base + 1, nil
}

func account(user string) (int, int, error) {
	raw, group, found := strings.Cut(user, ":")
	if !found {
		return 0, 0, fmt.Errorf("TRAINSHARD_CONTAINER_USER %q must be uid:gid", user)
	}
	uid, err := strconv.Atoi(raw)
	if err != nil {
		return 0, 0, fmt.Errorf("TRAINSHARD_CONTAINER_USER %q: %w", user, err)
	}
	gid, err := strconv.Atoi(group)
	if err != nil {
		return 0, 0, fmt.Errorf("TRAINSHARD_CONTAINER_USER %q: %w", user, err)
	}
	if uid == 0 || gid == 0 {
		return 0, 0, fmt.Errorf("TRAINSHARD_CONTAINER_USER %q: nothing runs as root", user)
	}
	return uid, gid, nil
}

func nodeRefs(participant vo.Participant, list string) ([]vo.NodeRef, error) {
	nodes := make([]vo.NodeRef, 0)
	for _, id := range strings.Split(list, ",") {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		node, err := vo.ParseNodeRef(string(participant), id)
		if err != nil {
			return nil, err
		}
		nodes = append(nodes, node)
	}
	return nodes, nil
}

func env(name, fallback string) string {
	if value, found := os.LookupEnv("TRAINSHARD_" + name); found {
		return value
	}
	return fallback
}

func list(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	entries := strings.Split(raw, ",")
	for i, entry := range entries {
		entries[i] = strings.TrimSpace(entry)
	}
	return entries
}

func number(name string, fallback int64) (int64, error) {
	raw := env(name, "")
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("TRAINSHARD_%s: %w", name, err)
	}
	return value, nil
}

func duration(name string, fallback time.Duration) (time.Duration, error) {
	raw := env(name, "")
	if raw == "" {
		return fallback, nil
	}
	value, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("TRAINSHARD_%s: %w", name, err)
	}
	return value, nil
}
