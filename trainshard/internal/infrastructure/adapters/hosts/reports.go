package hosts

import (
	"context"
	"net/http"
	"time"

	"trainshard/internal/contract"
	"trainshard/internal/domain/run"
	"trainshard/internal/domain/shared/vo"
)

const reportDeadline = time.Minute

func (c *Client) Report(ctx context.Context, participant vo.Participant, shardID vo.ShardID, nodes []vo.NodeRef) ([]run.NodeReport, error) {
	id := vo.NewRequestID()
	body := contract.ReportRequest{Command: fromCommand(run.HostCommand{
		Shard:     shardID,
		Nodes:     nodes,
		RequestID: id,
		Deadline:  c.clock.Now().Add(reportDeadline),
	})}

	var result contract.ReportResult
	path := toPath(contract.PathReport, shardID, "")
	if err := c.call(ctx, participant, http.MethodPost, path, id, body, &result); err != nil {
		return nil, err
	}
	return toReports(participant, result.Items)
}
