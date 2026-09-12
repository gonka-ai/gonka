package docker

import (
	"context"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
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

const (
	workdir = "/workspace"
	tmpdir  = "/tmp"
	rundir  = "/run"

	runBytes = 16 << 20
)

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
	sealed, err := c.sealed(ctx, spec)
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
		Binds:          append(binds, sealed...),
		NetworkMode:    mode,
		CapDrop:        []string{"ALL"},
		SecurityOpt:    []string{"no-new-privileges"},
		CgroupnsMode:   container.CgroupnsModePrivate,
		IpcMode:        container.IPCModePrivate,
		ReadonlyRootfs: true,
		Tmpfs:          c.scratch(),
		Init:           &init,
		ShmSize:        c.cfg.ShmBytes,
		RestartPolicy:  container.RestartPolicy{Name: container.RestartPolicyDisabled},
		LogConfig:      c.rolled(),
		Resources: container.Resources{
			Memory:         c.cfg.MemoryBytes,
			NanoCPUs:       c.cfg.NanoCPUs,
			PidsLimit:      &pids,
			Ulimits:        []*container.Ulimit{{Name: "core", Soft: 0, Hard: 0}},
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
	if err := os.RemoveAll(c.mountsPath(shardID, node)); err != nil {
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

// mountsPath sits beside the volume rather than in it, out of the run's own reach
func (c *Client) mountsPath(shardID vo.ShardID, node vo.NodeRef) string {
	return c.volumePath(shardID, node) + ".mounts"
}

func (c *Client) volumePath(shardID vo.ShardID, node vo.NodeRef) string {
	return filepath.Join(c.cfg.VolumeRoot, shardID.String(), string(node.NodeID))
}

// environment points the home directory at the workspace: the root is read only, and the caches a
// training image keeps under the home would otherwise have nowhere to go
func environment(values map[string]string) []string {
	merged := map[string]string{"HOME": workdir}
	maps.Copy(merged, values)

	env := make([]string, 0, len(merged))
	for name, value := range merged {
		env = append(env, name+"="+value)
	}
	sort.Strings(env)
	return env
}

// scratch gives the run the writable places outside its volume that it still needs, in memory,
// where they are held against the run's own memory limit and never reach the host disk
func (c *Client) scratch() map[string]string {
	return map[string]string{
		tmpdir: fmt.Sprintf("size=%d,mode=1777", c.cfg.TmpBytes),
		rundir: fmt.Sprintf("size=%d,mode=755", runBytes),
	}
}

// rolled bounds what the engine keeps of everything a run prints, which it otherwise stores whole
// and unmetered on the host disk
func (c *Client) rolled() container.LogConfig {
	return container.LogConfig{Type: "json-file", Config: map[string]string{
		"max-size": strconv.FormatInt(c.cfg.LogFileBytes, 10),
		"max-file": strconv.Itoa(c.cfg.LogFiles),
	}}
}

// binds hands the run its workspace, and read only copies of the files the engine would otherwise
// give it writable on the host disk and outside its quota
func (c *Client) binds(spec run.ContainerSpec) ([]string, error) {
	dir := c.mountsPath(spec.Shard, spec.Node)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}

	binds := []string{c.volumePath(spec.Shard, spec.Node) + ":" + workdir}
	for _, file := range etcFiles(spec) {
		path := filepath.Join(dir, file.name)
		if err := os.WriteFile(path, []byte(file.body), 0o644); err != nil {
			return nil, err
		}
		binds = append(binds, path+":/etc/"+file.name+":ro")
	}
	return binds, nil
}

// sealed covers the volumes an image declares of its own, which the engine would otherwise back
// with storage it manages and nobody meters
func (c *Client) sealed(ctx context.Context, spec run.ContainerSpec) ([]string, error) {
	image, present, err := c.inspectImage(ctx, spec.Run.Image.String())
	if err != nil || !present || image.Config == nil || len(image.Config.Volumes) == 0 {
		return nil, err
	}

	dir := filepath.Join(c.mountsPath(spec.Shard, spec.Node), "sealed")
	if err := os.MkdirAll(dir, 0o555); err != nil {
		return nil, err
	}

	binds := make([]string, 0, len(image.Config.Volumes))
	for _, path := range slices.Sorted(maps.Keys(image.Config.Volumes)) {
		binds = append(binds, dir+":"+path+":ro")
	}
	return binds, nil
}

type etcFile struct {
	name string
	body string
}

// etcFiles carry the names a run may reach, and an empty resolver, which is what a run with no
// dns of its own is meant to have
func etcFiles(spec run.ContainerSpec) []etcFile {
	hosts := []string{"127.0.0.1\tlocalhost", "::1\tlocalhost ip6-localhost ip6-loopback"}
	for _, host := range spec.Hosts {
		hosts = append(hosts, host.IP+"\t"+host.Name)
	}
	return []etcFile{
		{"hosts", strings.Join(hosts, "\n") + "\n"},
		{"hostname", string(spec.Node.NodeID) + "\n"},
		{"resolv.conf", ""},
	}
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
