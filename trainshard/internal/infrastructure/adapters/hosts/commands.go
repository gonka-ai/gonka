package hosts

import (
	"context"
	"net/http"

	"trainshard/internal/contract"
	"trainshard/internal/domain/run"
	"trainshard/internal/domain/shared/vo"
)

func (c *Client) Deploy(ctx context.Context, participant vo.Participant, call run.DeployCall) ([]run.NodeResult, error) {
	body := fromDeploy(call)

	var result contract.NodesResult
	path := toPath(contract.PathDeploy, call.Shard, "")
	if err := c.call(ctx, participant, http.MethodPost, path, call.RequestID, body, &result); err != nil {
		return nil, err
	}
	return toNodeResults(participant, result.Items), nil
}

func (c *Client) Start(ctx context.Context, participant vo.Participant, call run.HostCommand) ([]run.NodeResult, error) {
	var result contract.NodesResult
	path := toPath(contract.PathStart, call.Shard, "")
	if err := c.call(ctx, participant, http.MethodPost, path, call.RequestID, contract.StartRequest{Command: fromCommand(call)}, &result); err != nil {
		return nil, err
	}
	return toNodeResults(participant, result.Items), nil
}

func (c *Client) Stop(ctx context.Context, participant vo.Participant, call run.StopCall) ([]run.NodeResult, error) {
	body := contract.StopRequest{
		Command:      fromCommand(call.HostCommand),
		GraceSeconds: int(call.Grace.Seconds()),
	}

	var result contract.NodesResult
	path := toPath(contract.PathStop, call.Shard, "")
	if err := c.call(ctx, participant, http.MethodPost, path, call.RequestID, body, &result); err != nil {
		return nil, err
	}
	return toNodeResults(participant, result.Items), nil
}

func (c *Client) Status(ctx context.Context, participant vo.Participant, call run.HostCommand) ([]run.NodeStatus, error) {
	var result contract.StatusResult
	path := toPath(contract.PathStatus, call.Shard, "")
	if err := c.call(ctx, participant, http.MethodPost, path, call.RequestID, contract.StatusRequest{Command: fromCommand(call)}, &result); err != nil {
		return nil, err
	}
	return toNodeStatuses(participant, result.Items), nil
}
