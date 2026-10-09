package poc

import (
	"encoding/base64"
	"encoding/hex"
	"sync"

	"decentralized-api/broker"
	"decentralized-api/chainphase"

	"github.com/productscience/inference/x/inference/types"
)

// ChallengeCommitLeadBlocks stops generation before finish so the last commit can land.
const ChallengeCommitLeadBlocks int64 = 4

// OpenChallenges is the transient view of open challenges, replaced every synced block.
var OpenChallenges = NewChallengeCache()

func init() {
	broker.SetChallengeOverlay(OpenChallenges)
	broker.SetChallengeCommitLeadBlocks(ChallengeCommitLeadBlocks)
}

type ChallengeCache struct {
	mu            sync.RWMutex
	self          string
	list          []*types.OpenPoCChallenge
	minPunishable int64
}

func NewChallengeCache() *ChallengeCache {
	return &ChallengeCache{}
}

func cloneChallenge(ch *types.OpenPoCChallenge) *types.OpenPoCChallenge {
	if ch == nil {
		return nil
	}
	clone := *ch
	if ch.Challenge != nil {
		stored := *ch.Challenge
		if len(stored.Seed) > 0 {
			stored.Seed = append([]byte(nil), stored.Seed...)
		}
		clone.Challenge = &stored
	}
	if len(ch.Commits) > 0 {
		clone.Commits = make([]*types.PoCV2StoreCommit, len(ch.Commits))
		copy(clone.Commits, ch.Commits)
	}
	return &clone
}

func (c *ChallengeCache) Replace(self string, list []*types.OpenPoCChallenge, minPunishable int64) {
	copied := make([]*types.OpenPoCChallenge, 0, len(list))
	for _, ch := range list {
		if ch == nil {
			continue
		}
		if ch.Challenge != nil && ch.Challenge.State != types.PoCChallengeState_POC_CHALLENGE_STATE_OPEN {
			continue
		}
		copied = append(copied, cloneChallenge(ch))
	}
	c.mu.Lock()
	c.self = self
	c.list = copied
	c.minPunishable = minPunishable
	c.mu.Unlock()
}

func (c *ChallengeCache) Reset() {
	c.Replace("", nil, 0)
}

func (c *ChallengeCache) MinPunishable() int64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.minPunishable <= 0 {
		return types.DefaultMinPunishableSegmentBlocks
	}
	return c.minPunishable
}

func (c *ChallengeCache) Self() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.self
}

func (c *ChallengeCache) List() []*types.OpenPoCChallenge {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]*types.OpenPoCChallenge, 0, len(c.list))
	for _, ch := range c.list {
		out = append(out, cloneChallenge(ch))
	}
	return out
}

func (c *ChallengeCache) Own(addr string) *types.OpenPoCChallenge {
	if addr == "" {
		return nil
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, ch := range c.list {
		if ch != nil && ch.Target() == addr {
			return cloneChallenge(ch)
		}
	}
	return nil
}

func (c *ChallengeCache) UnderChallenge() *types.OpenPoCChallenge {
	ch := c.Own(c.Self())
	if ch == nil || !ch.Generating {
		return nil
	}
	return ch
}

func (c *ChallengeCache) VoteFor(addr string, stageHeight int64) *types.OpenPoCChallenge {
	ch := c.Own(addr)
	if ch == nil || ch.StartHeight() != stageHeight {
		return nil
	}
	return ch
}

func (c *ChallengeCache) Punishable() []*types.OpenPoCChallenge {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]*types.OpenPoCChallenge, 0, len(c.list))
	for _, ch := range c.list {
		if ch == nil {
			continue
		}
		if ch.Finish-ch.StartHeight() < c.minOrDefaultLocked() {
			continue
		}
		out = append(out, cloneChallenge(ch))
	}
	return out
}

func SeedHex(seed []byte) string {
	if len(seed) == 0 {
		return ""
	}
	return hex.EncodeToString(seed)
}

// AccountPubKeyToHex converts AccountByAddress.Pubkey (base64 of secp256k1
// account key bytes) to the hex string MLNode and regular HexPubKey use.
func AccountPubKeyToHex(accountPubKeyB64 string) string {
	if accountPubKeyB64 == "" {
		return ""
	}
	b, err := base64.StdEncoding.DecodeString(accountPubKeyB64)
	if err != nil || len(b) == 0 {
		return ""
	}
	return hex.EncodeToString(b)
}

func InVoteWindow(epochState *chainphase.EpochState) bool {
	return epochState != nil && epochState.IsPoCVoteWindow()
}

// GeneratingChallengeWork is this participant's challenge while it should
// generate challenge PoC. Nil in vote windows.
func GeneratingChallengeWork(epochState *chainphase.EpochState) *types.OpenPoCChallenge {
	if epochState == nil || epochState.IsNilOrNotSynced() || InVoteWindow(epochState) {
		return nil
	}
	ch := OpenChallenges.UnderChallenge()
	if ch == nil || ch.StartHeight() <= 0 {
		return nil
	}
	if ch.Finish <= 0 || epochState.CurrentBlock.Height >= ch.Finish {
		return nil
	}
	return ch
}

func (c *ChallengeCache) minOrDefaultLocked() int64 {
	if c.minPunishable <= 0 {
		return types.DefaultMinPunishableSegmentBlocks
	}
	return c.minPunishable
}

func ShouldValidateChallenge(ch *types.OpenPoCChallenge, height int64) bool {
	if ch == nil {
		return false
	}
	if ch.Finish-ch.StartHeight() < OpenChallenges.MinPunishable() {
		return false
	}
	return height >= ch.Finish
}
