package docker

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
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

	binds, err := c.binds(spec)
	if err != nil {
		return err
	}

	init, pids := true, c.cfg.PidsLimit
	marks := labels(spec.Shard, spec.Node, "run")
	marks[labelRevision] = strconv.Itoa(spec.Revision)
	config := &container.Config{
		Image:      spec.Run.Image.String(),
		Cmd:        spec.Run.Command,
		Env:        environment(spec.Run.Env),
		User:       c.cfg.User,
		WorkingDir: workdir,
		Labels:     marks,
	}
	host := &container.HostConfig{
		Binds:         binds,
		NetworkMode:   mode,
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
			DeviceRequests: c.gpuRequests(spec.Run.Resources.GPUs),
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
	if err := os.Remove(c.hostsPath(shardID, node)); err != nil && !errors.Is(err, fs.ErrNotExist) {
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

func (c *Client) hostsPath(shardID vo.ShardID, node vo.NodeRef) string {
	return c.volumePath(shardID, node) + ".hosts"
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

// binds hands the run its workspace, and the names it may reach as a hosts file: the run has no dns,
// and the engine refuses host entries to a container that lives in another one's network
func (c *Client) binds(spec run.ContainerSpec) ([]string, error) {
	binds := []string{c.volumePath(spec.Shard, spec.Node) + ":" + workdir}
	if len(spec.Hosts) == 0 {
		return binds, nil
	}

	lines := []string{"127.0.0.1\tlocalhost", "::1\tlocalhost ip6-localhost ip6-loopback"}
	for _, host := range spec.Hosts {
		lines = append(lines, host.IP+"\t"+host.Name)
	}

	path := c.hostsPath(spec.Shard, spec.Node)
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		return nil, err
	}
	return append(binds, path+":/etc/hosts:ro"), nil
}

// gpuRequests asks for cards the way `docker run --gpus` does, leaving the engine to name the vendor:
// engines from 28 on hand that to the device interface the driver installs, and naming a driver
// ourselves gets the run refused there. A kind names the devices outright, for a host whose engine
// only knows them by name
func (c *Client) gpuRequests(count int) []container.DeviceRequest {
	if count <= 0 {
		return nil
	}
	if c.cfg.GPUKind == "" {
		return []container.DeviceRequest{{Count: count, Capabilities: [][]string{{"gpu"}}}}
	}

	ids := make([]string, count)
	for index := range ids {
		ids[index] = fmt.Sprintf("%s=%d", c.cfg.GPUKind, index)
	}
	return []container.DeviceRequest{{Driver: "cdi", DeviceIDs: ids}}
}

func toContainerInfo(found container.InspectResponse) (run.ContainerInfo, error) {
	image, err := vo.ParseImageDigest(found.Config.Image)
	if err != nil {
		return run.ContainerInfo{}, fmt.Errorf("container %s: %w", found.Name, err)
	}

	revision, _ := strconv.Atoi(found.Config.Labels[labelRevision])
	info := run.ContainerInfo{State: toContainerState(found.State.Status), Image: image, Revision: revision}
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
