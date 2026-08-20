package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"sort"
	"strconv"
	"time"

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

	body := createRequest{
		Image:      spec.Run.Image.String(),
		Cmd:        spec.Run.Command,
		Env:        environment(spec.Run.Env),
		User:       c.cfg.User,
		WorkingDir: workdir,
		Labels:     labels(spec.Shard, spec.Node, "run"),
		HostConfig: hostConfig{
			Binds:          []string{c.volumePath(spec.Shard, spec.Node) + ":" + workdir},
			NetworkMode:    mode,
			ExtraHosts:     extraHosts(spec.Hosts),
			CapDrop:        []string{"ALL"},
			SecurityOpt:    []string{"no-new-privileges"},
			CgroupnsMode:   "private",
			IpcMode:        "private",
			Init:           true,
			Memory:         c.cfg.MemoryBytes,
			NanoCPUs:       c.cfg.NanoCPUs,
			PidsLimit:      c.cfg.PidsLimit,
			ShmSize:        c.cfg.ShmBytes,
			RestartPolicy:  restartPolicy{Name: "no"},
			DeviceRequests: gpuRequests(spec.Run.Resources.GPUs),
		},
	}

	query := url.Values{"name": {containerName(spec.Shard, spec.Node)}}
	if _, err := c.call(ctx, http.MethodPost, "/containers/create", query, body, nil); err != nil {
		return err
	}
	c.log.Info("created container", "node_id", spec.Node.NodeID, "image_digest", spec.Run.Image.String(), "network", mode)
	return nil
}

func (c *Client) Start(ctx context.Context, shardID vo.ShardID, node vo.NodeRef) error {
	return c.act(ctx, shardID, node, "/start", nil, "started container")
}

func (c *Client) Stop(ctx context.Context, shardID vo.ShardID, node vo.NodeRef, grace time.Duration) error {
	seconds := strconv.Itoa(int(grace.Round(time.Second).Seconds()))
	return c.act(ctx, shardID, node, "/stop", url.Values{"t": {seconds}}, "stopped container")
}

func (c *Client) Remove(ctx context.Context, shardID vo.ShardID, node vo.NodeRef) error {
	name := containerName(shardID, node)
	status, err := c.call(ctx, http.MethodDelete, "/containers/"+name, url.Values{"v": {"false"}}, nil, nil)
	if status == http.StatusNotFound {
		return nil
	}
	if err != nil {
		return err
	}
	c.log.Info("removed container", "node_id", node.NodeID)
	return nil
}

func (c *Client) Shards(ctx context.Context, node vo.NodeRef) ([]vo.ShardID, error) {
	filters, err := json.Marshal(map[string][]string{"label": {labelNode + "=" + string(node.NodeID)}})
	if err != nil {
		return nil, err
	}

	var found []struct {
		Labels map[string]string `json:"Labels"`
	}
	query := url.Values{"all": {"1"}, "filters": {string(filters)}}
	if _, err := c.call(ctx, http.MethodGet, "/containers/json", query, nil, &found); err != nil {
		return nil, err
	}

	shards := make([]vo.ShardID, 0, len(found))
	for _, entry := range found {
		shardID, err := vo.ParseShardID(entry.Labels[labelShard])
		if err != nil {
			continue
		}
		shards = append(shards, shardID)
	}
	return shards, nil
}

func (c *Client) act(ctx context.Context, shardID vo.ShardID, node vo.NodeRef, action string, query url.Values, message string) error {
	name := containerName(shardID, node)
	status, err := c.call(ctx, http.MethodPost, "/containers/"+name+action, query, nil, nil)
	switch {
	case status == http.StatusNotModified, status == http.StatusNotFound:
		return nil
	case err != nil:
		return err
	}
	c.log.Info(message, "node_id", node.NodeID)
	return nil
}

func (c *Client) networkMode(ctx context.Context, shardID vo.ShardID, node vo.NodeRef) (string, error) {
	sandbox := sandboxName(shardID, node)
	found, present, err := c.inspectContainer(ctx, sandbox)
	if err != nil {
		return "", err
	}
	if !present || !found.State.Running {
		return "none", nil
	}
	return "container:" + sandbox, nil
}

func (c *Client) inspectContainer(ctx context.Context, name string) (containerInspect, bool, error) {
	var found containerInspect
	status, err := c.call(ctx, http.MethodGet, "/containers/"+name+"/json", nil, nil, &found)
	if status == http.StatusNotFound {
		return containerInspect{}, false, nil
	}
	if err != nil {
		return containerInspect{}, false, err
	}
	return found, true, nil
}

func (c *Client) ContainerID(ctx context.Context, shardID vo.ShardID, node vo.NodeRef) (string, bool, error) {
	found, present, err := c.inspectContainer(ctx, containerName(shardID, node))
	if err != nil || !present {
		return "", false, err
	}
	return found.ID, true, nil
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

func gpuRequests(count int) []deviceRequest {
	if count <= 0 {
		return nil
	}
	return []deviceRequest{{
		Driver:       "nvidia",
		Count:        count,
		Capabilities: [][]string{{"gpu"}},
	}}
}

func toContainerInfo(found containerInspect) (run.ContainerInfo, error) {
	image, err := vo.ParseImageDigest(found.Config.Image)
	if err != nil {
		return run.ContainerInfo{}, fmt.Errorf("container %s: %w", found.Name, err)
	}

	info := run.ContainerInfo{State: toContainerState(found.State), Image: image}
	if info.State == vo.ContainerExited {
		code := found.State.ExitCode
		info.ExitCode = &code
	}
	return info, nil
}

func toContainerState(state containerState) vo.ContainerState {
	switch state.Status {
	case "created":
		return vo.ContainerCreated
	case "running", "paused", "restarting":
		return vo.ContainerRunning
	default:
		return vo.ContainerExited
	}
}
