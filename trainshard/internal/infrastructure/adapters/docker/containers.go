package docker

import (
	"context"
	"errors"
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
		// MemorySwap equal to Memory, or the engine lets the run swap as much again
		Resources: container.Resources{
			Memory:         c.cfg.MemoryBytes,
			MemorySwap:     c.cfg.MemoryBytes,
			NanoCPUs:       c.cfg.NanoCPUs,
			PidsLimit:      &pids,
			Ulimits:        []*container.Ulimit{{Name: "core", Soft: 0, Hard: 0}},
			DeviceRequests: c.gpuRequests(spec.Run.Resources.GPUs),
		},
	}

	bounded, cancel := c.bounded(ctx)
	defer cancel()

	name := containerName(spec.Shard, spec.Node)
	if _, err := c.engine.ContainerCreate(bounded, client.ContainerCreateOptions{
		Name:       name,
		Config:     config,
		HostConfig: host,
	}); err != nil {
		// an engine that timed out may still have made the container, and its /etc files live here
		if _, present, inspectErr := c.inspectContainer(ctx, name); present || inspectErr != nil {
			return err
		}
		return errors.Join(err, os.RemoveAll(c.mountsPath(spec.Shard, spec.Node)))
	}
	c.log.Info("created container", "node_id", spec.Node.NodeID, "image_digest", spec.Run.Image.Short(), "network", mode)
	return nil
}

func (c *Client) Start(ctx context.Context, shardID vo.ShardID, node vo.NodeRef) error {
	ctx, cancel := c.bounded(ctx)
	defer cancel()

	if _, err := c.engine.ContainerStart(ctx, containerName(shardID, node), client.ContainerStartOptions{}); err != nil && !cerrdefs.IsNotModified(err) {
		return err
	}
	c.log.Info("started container", "node_id", node.NodeID)
	return nil
}

func (c *Client) Stop(ctx context.Context, shardID vo.ShardID, node vo.NodeRef, grace time.Duration) error {
	// the engine answers only once the run is down, which can take the whole grace
	ctx, cancel := context.WithTimeout(ctx, grace+c.cfg.Timeout)
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

// beside the volume rather than in it, out of the run's own reach
func (c *Client) mountsPath(shardID vo.ShardID, node vo.NodeRef) string {
	return c.volumePath(shardID, node) + ".mounts"
}

func (c *Client) volumePath(shardID vo.ShardID, node vo.NodeRef) string {
	return filepath.Join(c.cfg.VolumeRoot, shardID.String(), string(node.NodeID))
}

// HOME is the workspace: the root is read only, and a training image keeps its caches under the home
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

// in memory, so scratch counts against the run's memory limit and never reaches the host disk;
// /tmp is exec because jit compilers load what they build there
func (c *Client) scratch() map[string]string {
	return map[string]string{
		tmpdir: fmt.Sprintf("size=%d,mode=1777,exec", c.cfg.TmpBytes),
		rundir: fmt.Sprintf("size=%d,mode=755", runBytes),
	}
}

// the engine otherwise keeps everything a run prints, whole and unmetered, on the host disk
func (c *Client) rolled() container.LogConfig {
	return container.LogConfig{Type: "json-file", Config: map[string]string{
		"max-size": strconv.FormatInt(c.cfg.LogFileBytes, 10),
		"max-file": strconv.Itoa(c.cfg.LogFiles),
	}}
}

// the engine would otherwise hand the run its /etc files writable, on the host disk and outside its quota
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

// the engine would otherwise back an image's own volumes with storage nobody meters
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

// the resolver stays empty: the run has no dns, only the names egress pinned
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

// no driver named, the way `docker run --gpus` asks: engines from 28 on refuse a request that names one.
// A kind names the devices by cdi id, for an engine that only knows them that way
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
