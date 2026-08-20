package main

import (
	"fmt"
	"log/slog"

	"trainshard/internal/domain/mesh"
	"trainshard/internal/domain/run"
	"trainshard/internal/domain/shared/ports"
	"trainshard/internal/infrastructure/adapters/docker"
	"trainshard/internal/infrastructure/adapters/memory"
	"trainshard/internal/infrastructure/adapters/netns"
	"trainshard/internal/infrastructure/adapters/nvidia"
	"trainshard/internal/infrastructure/adapters/xfsquota"
)

type parts struct {
	images     run.Images
	containers run.Containers
	volumes    run.Volumes
	gpu        run.GPU
	network    mesh.Network
	egress     run.Egress
	streams    run.Streams
	probe      ports.Probe
}

func machinery(cfg config, clock ports.Clock, log *slog.Logger) (parts, error) {
	switch cfg.machine {
	case "memory":
		fake := memory.New(log, cfg.inventory)
		return parts{
			images:     fake,
			containers: fake,
			volumes:    fake,
			gpu:        fake,
			network:    fake.Mesh(),
			egress:     fake,
			streams:    fake,
			probe:      fake,
		}, nil
	case "docker":
		engine, err := docker.New(docker.Config{
			Socket:       cfg.dockerSocket,
			VolumeRoot:   cfg.volumeRoot,
			User:         cfg.containerUser,
			SandboxImage: cfg.sandboxImage,
			MemoryBytes:  cfg.memoryBytes,
			NanoCPUs:     cfg.nanoCPUs,
		}, log)
		if err != nil {
			return parts{}, err
		}
		gpus := nvidia.New(nvidia.Config{SMI: cfg.nvidiaSMI}, engine, log)
		volumes := xfsquota.New(xfsquota.Config{
			Root:  cfg.volumeRoot,
			Mount: cfg.volumeMount,
			UID:   cfg.containerUID,
			GID:   cfg.containerGID,
		}, log)
		network := netns.New(netns.Config{
			Nodes:       cfg.nodes,
			Endpoint:    cfg.meshEndpoint,
			PortBase:    cfg.meshPortBase,
			KeyDir:      cfg.meshKeyDir,
			DeniedCIDRs: cfg.deniedCIDRs,
		}, engine, clock, log)

		return parts{
			images:     engine,
			containers: engine,
			volumes:    volumes,
			gpu:        gpus,
			network:    network,
			egress:     network,
			streams:    engine,
			probe:      probe{Client: engine, Volumes: volumes, Network: network},
		}, nil
	default:
		return parts{}, fmt.Errorf("machine %q is neither memory nor docker", cfg.machine)
	}
}

type probe struct {
	*docker.Client
	*xfsquota.Volumes
	*netns.Network
}
