package chain

import (
	"context"
	"sync"
	"time"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"

	"trainshard/internal/domain/shared/vo"
)

const (
	grantKeeps   = 10 * time.Minute
	refusalKeeps = 30 * time.Second
)

var trainingGrants = []string{
	types.WarmKeyGrantMarkerTypeURL,
	sdk.MsgTypeURL(&types.MsgRefreshTrainingNodeOptIn{}),
	sdk.MsgTypeURL(&types.MsgAutokickTrainshardNode{}),
}

type grantKey struct {
	participant vo.Participant
	signer      vo.Address
	msgType     string
}

type grantAnswer struct {
	held  bool
	until time.Time
}

type grants struct {
	mu      sync.Mutex
	answers map[grantKey]grantAnswer
}

func (c *Client) Speaks(ctx context.Context, participant vo.Participant, signer vo.Address) (bool, error) {
	if vo.Address(participant) == signer {
		return true, nil
	}
	return c.granted(ctx, participant, signer, types.WarmKeyGrantMarkerTypeURL)
}

func (c *Client) MissingGrants(ctx context.Context, participant vo.Participant, signer vo.Address) ([]string, error) {
	if vo.Address(participant) == signer {
		return nil, nil
	}
	missing := make([]string, 0)
	for _, msgType := range trainingGrants {
		held, err := c.granted(ctx, participant, signer, msgType)
		if err != nil {
			return nil, err
		}
		if !held {
			missing = append(missing, msgType)
		}
	}
	return missing, nil
}

func (c *Client) granted(ctx context.Context, participant vo.Participant, signer vo.Address, msgType string) (bool, error) {
	key := grantKey{participant: participant, signer: signer, msgType: msgType}
	now := time.Now()

	c.grants.mu.Lock()
	answer, cached := c.grants.answers[key]
	c.grants.mu.Unlock()
	if cached && now.Before(answer.until) {
		return answer.held, nil
	}

	reply, err := c.query.GranteesByMessageType(ctx, &types.QueryGranteesByMessageTypeRequest{
		GranterAddress: string(participant),
		MessageTypeUrl: msgType,
	})
	if err != nil {
		return false, err
	}
	held := false
	for _, grantee := range reply.Grantees {
		if grantee != nil && vo.Address(grantee.Address) == signer {
			held = true
			break
		}
	}

	keeps := refusalKeeps
	if held {
		keeps = grantKeeps
	}
	c.grants.mu.Lock()
	c.grants.answers[key] = grantAnswer{held: held, until: now.Add(keeps)}
	c.grants.mu.Unlock()
	return held, nil
}
