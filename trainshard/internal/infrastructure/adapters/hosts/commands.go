package hosts

import (
	"context"
	"net/http"
	"time"

	"trainshard/internal/contract"
	"trainshard/internal/domain/run"
	"trainshard/internal/domain/shared/vo"
)

func (c *Client) Deploy(ctx context.Context, host vo.Host, call run.DeployCall) ([]run.NodeResult, error) {
	body := fromDeploy(call)

	var result contract.NodesResult
	path := toPath(contract.PathDeploy, call.Shard, "")
	if err := c.call(ctx, host, http.MethodPost, path, call.RequestID, body, &result); err != nil {
		return nil, err
	}
	return toNodeResults(host.Participant, result.Items), nil
}

func (c *Client) Start(ctx context.Context, host vo.Host, call run.HostCommand) ([]run.NodeResult, error) {
	var result contract.NodesResult
	path := toPath(contract.PathStart, call.Shard, "")
	if err := c.call(ctx, host, http.MethodPost, path, call.RequestID, contract.StartRequest{Command: fromCommand(call)}, &result); err != nil {
		return nil, err
	}
	return toNodeResults(host.Participant, result.Items), nil
}

func (c *Client) Stop(ctx context.Context, host vo.Host, call run.StopCall) ([]run.NodeResult, error) {
	body := contract.StopRequest{
		Command: fromCommand(call.HostCommand),
	}
	if call.GraceGiven {
		// rounded up: a part of a second sent as 0 would stop the container at once
		seconds := int(call.Grace / time.Second)
		if call.Grace%time.Second > 0 {
			seconds++
		}
		body.GraceSeconds = &seconds
	}

	var result contract.NodesResult
	path := toPath(contract.PathStop, call.Shard, "")
	if err := c.call(ctx, host, http.MethodPost, path, call.RequestID, body, &result); err != nil {
		return nil, err
	}
	return toNodeResults(host.Participant, result.Items), nil
}

func (c *Client) Status(ctx context.Context, host vo.Host, call run.HostCommand) ([]run.NodeStatus, error) {
	var result contract.StatusResult
	path := toPath(contract.PathStatus, call.Shard, "")
	if err := c.call(ctx, host, http.MethodPost, path, call.RequestID, contract.StatusRequest{Command: fromCommand(call)}, &result); err != nil {
		return nil, err
	}
	return toNodeStatuses(host.Participant, result.Items), nil
}
