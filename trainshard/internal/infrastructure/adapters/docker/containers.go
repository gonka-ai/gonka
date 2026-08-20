package docker

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"

	"trainshard/internal/domain/run"
	"trainshard/internal/domain/shared/vo"
)

const workdir = "/workspace"

func (c *Client) Inspect(ctx context.Context, shardID vo.ShardID, node vo.NodeRef) (run.ContainerInfo, error) {
	found, present, err := c.inspectContainer(ctx, containerName(shardID, node))
	if err != nil {
		return run.ContainerInfo{}, err
	}
	if !present {
		return run.ContainerInfo{State: vo.ContainerAbsent}, nil
	}
	return toContainerInfo(found)
}

func (c *Client) Create(ctx context.Context, spec run.ContainerSpec) error {
	mode, err := c.networkMode(ctx, spec.Shard, spec.Node)
	if err != nil {
		return err
	}

	init, pids := true, c.cfg.PidsLimit
	config := &container.Config{
		Image:      spec.Run.Image.String(),
		Cmd:        spec.Run.Command,
		Env:        environment(spec.Run.Env),
		User:       c.cfg.User,
		WorkingDir: workdir,
		Labels:     labels(spec.Shard, spec.Node, "run"),
	}
	host := &container.HostConfig{
		Binds:         []string{c.volumePath(spec.Shard, spec.Node) + ":" + workdir},
		NetworkMode:   mode,
		ExtraHosts:    extraHosts(spec.Hosts),
		CapDrop:       []string{"ALL"},
		SecurityOpt:   []string{"no-new-privileges"},
		CgroupnsMode:  container.CgroupnsModePrivate,
		IpcMode:       container.IPCModePrivate,
		Init:          &init,
		ShmSize:       c.cfg.ShmBytes,
		RestartPolicy: container.RestartPolicy{Name: container.RestartPolicyDisabled},
		Resources: container.Resources{
			Memory:         c.cfg.MemoryBytes,
			NanoCPUs:       c.cfg.NanoCPUs,
			PidsLimit:      &pids,
			DeviceRequests: gpuRequests(spec.Run.Resources.GPUs),
		},
	}

	ctx, cancel := c.bounded(ctx)
	defer cancel()

	if _, err := c.engine.ContainerCreate(ctx, client.ContainerCreateOptions{
		Name:       containerName(spec.Shard, spec.Node),
		Config:     config,
		HostConfig: host,
	}); err != nil {
		return err
	}
	c.log.Info("created container", "node_id", spec.Node.NodeID, "image_digest", spec.Run.Image.Short(), "network", mode)
	return nil
}

func (c *Client) Start(ctx context.Context, shardID vo.ShardID, node vo.NodeRef) error {
	ctx, cancel := c.bounded(ctx)
	defer cancel()

	_, err := c.engine.ContainerStart(ctx, containerName(shardID, node), client.ContainerStartOptions{})
	if !settled(err) {
		return err
	}
	c.log.Info("started container", "node_id", node.NodeID)
	return nil
}

func (c *Client) Stop(ctx context.Context, shardID vo.ShardID, node vo.NodeRef, grace time.Duration) error {
	ctx, cancel := c.bounded(ctx)
	defer cancel()

	seconds := int(grace.Round(time.Second).Seconds())
	_, err := c.engine.ContainerStop(ctx, containerName(shardID, node), client.ContainerStopOptions{Timeout: &seconds})
	if !settled(err) {
		return err
	}
	c.log.Info("stopped container", "node_id", node.NodeID)
	return nil
}

func (c *Client) Remove(ctx context.Context, shardID vo.ShardID, node vo.NodeRef) error {
	ctx, cancel := c.bounded(ctx)
	defer cancel()

	_, err := c.engine.ContainerRemove(ctx, containerName(shardID, node), client.ContainerRemoveOptions{})
	if !settled(err) {
		return err
	}
	c.log.Info("removed container", "node_id", node.NodeID)
	return nil
}

func (c *Client) Shards(ctx context.Context, node vo.NodeRef) ([]vo.ShardID, error) {
	ctx, cancel := c.bounded(ctx)
	defer cancel()

	listed, err := c.engine.ContainerList(ctx, client.ContainerListOptions{
		All:     true,
		Filters: make(client.Filters).Add("label", labelNode+"="+string(node.NodeID)),
	})
	if err != nil {
		return nil, err
	}

	shards := make([]vo.ShardID, 0, len(listed.Items))
	for _, item := range listed.Items {
		shardID, err := vo.ParseShardID(item.Labels[labelShard])
		if err != nil {
			continue
		}
		shards = append(shards, shardID)
	}
	return shards, nil
}

func (c *Client) ContainerID(ctx context.Context, shardID vo.ShardID, node vo.NodeRef) (string, bool, error) {
	found, present, err := c.inspectContainer(ctx, containerName(shardID, node))
	if err != nil || !present {
		return "", false, err
	}
	return found.ID, true, nil
}

func (c *Client) networkMode(ctx context.Context, shardID vo.ShardID, node vo.NodeRef) (container.NetworkMode, error) {
	sandbox := sandboxName(shardID, node)
	found, present, err := c.inspectContainer(ctx, sandbox)
	if err != nil {
		return "", err
	}
	if !present || !found.State.Running {
		return container.NetworkMode("none"), nil
	}
	return container.NetworkMode("container:" + sandbox), nil
}

func (c *Client) inspectContainer(ctx context.Context, name string) (container.InspectResponse, bool, error) {
	ctx, cancel := c.bounded(ctx)
	defer cancel()

	found, err := c.engine.ContainerInspect(ctx, name, client.ContainerInspectOptions{})
	if cerrdefs.IsNotFound(err) {
		return container.InspectResponse{}, false, nil
	}
	if err != nil {
		return container.InspectResponse{}, false, err
	}
	if found.Container.State == nil || found.Container.Config == nil {
		return container.InspectResponse{}, false, fmt.Errorf("container %s: engine answered without state or config", name)
	}
	return found.Container, true, nil
}

func (c *Client) removeByName(ctx context.Context, name string) error {
	ctx, cancel := c.bounded(ctx)
	defer cancel()

	_, err := c.engine.ContainerRemove(ctx, name, client.ContainerRemoveOptions{Force: true})
	if settled(err) {
		return nil
	}
	return err
}

func (c *Client) volumePath(shardID vo.ShardID, node vo.NodeRef) string {
	return filepath.Join(c.cfg.VolumeRoot, shardID.String(), string(node.NodeID))
}

func environment(values map[string]string) []string {
	env := make([]string, 0, len(values))
	for name, value := range values {
		env = append(env, name+"="+value)
	}
	sort.Strings(env)
	return env
}

func extraHosts(pinned []run.PinnedHost) []string {
	hosts := make([]string, 0, len(pinned))
	for _, host := range pinned {
		hosts = append(hosts, host.Name+":"+host.IP)
	}
	return hosts
}

func gpuRequests(count int) []container.DeviceRequest {
	if count <= 0 {
		return nil
	}
	return []container.DeviceRequest{{
		Driver:       "nvidia",
		Count:        count,
		Capabilities: [][]string{{"gpu"}},
	}}
}

func toContainerInfo(found container.InspectResponse) (run.ContainerInfo, error) {
	image, err := vo.ParseImageDigest(found.Config.Image)
	if err != nil {
		return run.ContainerInfo{}, fmt.Errorf("container %s: %w", found.Name, err)
	}

	info := run.ContainerInfo{State: toContainerState(found.State.Status), Image: image}
	if info.State == vo.ContainerExited {
		code := found.State.ExitCode
		info.ExitCode = &code
	}
	return info, nil
}

func toContainerState(state container.ContainerState) vo.ContainerState {
	switch state {
	case container.StateCreated:
		return vo.ContainerCreated
	case container.StateRunning, container.StatePaused, container.StateRestarting:
		return vo.ContainerRunning
	default:
		return vo.ContainerExited
	}
}
