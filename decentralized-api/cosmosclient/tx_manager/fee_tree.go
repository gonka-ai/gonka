package tx_manager

import (
	"sync"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

// FeeTreeCache holds the on-chain fee-group tree DAPI needs to attach
// fees and size StoreCommit/HardwareDiff gas. Refreshed from the per-block
// Params query; a failed query leaves the last known-good snapshot.
type FeeTreeCache struct {
	mu sync.RWMutex

	enabled            map[string]struct{}
	groupPrice         map[string]uint64
	storeCommitRate    uint64
	storeCommitBase    uint64
	storeCommitRateRaw uint64
	storeCommitBaseRaw uint64
	hdGasPerByte       uint64
	hdUnitSize         uint64
	hasStoreCommitRate bool
	hasStoreCommitBase bool
	hasHDGasPerByte    bool
	loaded             bool // true after Load; distinguishes "never fetched" from "tree has no StoreCommit row"
	// Measured StoreCommit ante+handler gas from the once-per-stage dummy
	// Simulate. Survives Load() so a params refresh does not drop a good
	// measurement mid-window.
	storeCommitIntrinsic         uint64
	hasMeasuredIntrinsic         bool
	storeCommitCalibratedEntries uint
	storeCommitPrev              map[string]uint32 // model_id -> last committed count
	hardwarePrev                 []*types.HardwareNode
}

func newFeeTreeCache() *FeeTreeCache {
	return &FeeTreeCache{
		enabled:         map[string]struct{}{},
		groupPrice:      map[string]uint64{},
		storeCommitPrev: map[string]uint32{},
	}
}

func (c *FeeTreeCache) Load(fp *types.FeeParams) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.enabled = map[string]struct{}{}
	c.groupPrice = map[string]uint64{}
	c.storeCommitRate = 0
	c.storeCommitBase = 0
	c.storeCommitRateRaw = 0
	c.storeCommitBaseRaw = 0
	c.hdGasPerByte = 0
	c.hdUnitSize = 0
	c.hasStoreCommitRate = false
	c.hasStoreCommitBase = false
	c.hasHDGasPerByte = false
	c.loaded = true
	if fp == nil {
		return
	}
	for _, name := range fp.EnabledFeeGroups {
		c.enabled[name] = struct{}{}
	}
	storeCommitURL := sdk.MsgTypeURL(&types.MsgPoCV2StoreCommit{})
	hdURL := sdk.MsgTypeURL(&types.MsgSubmitHardwareDiff{})
	for _, g := range fp.Groups {
		if g == nil {
			continue
		}
		c.groupPrice[g.Name] = g.MinGasPrice
		for _, rule := range g.Msgs {
			if rule == nil {
				continue
			}
			switch rule.TypeUrl {
			case storeCommitURL:
				if d := rule.GetStoredDelta(); d != nil {
					c.hasStoreCommitRate = true
					c.storeCommitRateRaw = d.GasPerUnit
					c.storeCommitRate = withHeadroom(d.GasPerUnit, 1, 2) // 50% if nonzero; 0 stays 0
				}
				if rule.Base != nil {
					c.hasStoreCommitBase = true
					c.storeCommitBaseRaw = rule.Base.Gas
					c.storeCommitBase = withHeadroom(rule.Base.Gas, 1, 5) // 20% if nonzero; 0 stays 0
				}
			case hdURL:
				if b := rule.GetStoredBytes(); b != nil {
					c.hasHDGasPerByte = true
					c.hdGasPerByte = b.GasPerUnit
					if sz, ok := types.StoredBytesUnitSize(b.Unit); ok {
						c.hdUnitSize = sz
					}
				}
			}
		}
	}
}

func withHeadroom(v, extraNumer, extraDenom uint64) uint64 {
	if v == 0 || extraDenom == 0 {
		return 0
	}
	return v + v*extraNumer/extraDenom
}

func (c *FeeTreeCache) SetStoreCommitPrev(prev map[string]uint32) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.storeCommitPrev = map[string]uint32{}
	for k, v := range prev {
		c.storeCommitPrev[k] = v
	}
}

func (c *FeeTreeCache) SetStoreCommitIntrinsic(gas uint64, calibratedEntries uint) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.storeCommitIntrinsic = gas
	c.hasMeasuredIntrinsic = true
	c.storeCommitCalibratedEntries = calibratedEntries
}

func (c *FeeTreeCache) ClearStoreCommitIntrinsic() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.storeCommitIntrinsic = 0
	c.hasMeasuredIntrinsic = false
	c.storeCommitCalibratedEntries = 0
}

func (c *FeeTreeCache) RawStoreCommitLeaf() (rate, base uint64, loaded bool) {
	if c == nil {
		return 0, 0, false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.storeCommitRateRaw, c.storeCommitBaseRaw, c.loaded
}

func (c *FeeTreeCache) SetHardwarePrev(nodes []*types.HardwareNode) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.hardwarePrev = make([]*types.HardwareNode, 0, len(nodes))
	for _, n := range nodes {
		if n != nil {
			c.hardwarePrev = append(c.hardwarePrev, n)
		}
	}
}

func (c *FeeTreeCache) hints() GasHints {
	if c == nil {
		return GasHints{}
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	prev := make(map[string]uint32, len(c.storeCommitPrev))
	for k, v := range c.storeCommitPrev {
		prev[k] = v
	}
	hw := make([]*types.HardwareNode, len(c.hardwarePrev))
	copy(hw, c.hardwarePrev)
	return GasHints{
		FeeTreeLoaded: c.loaded,
		StoreCommit: StoreCommitGas{
			Prev:              prev,
			HasRate:           c.hasStoreCommitRate,
			HasBase:           c.hasStoreCommitBase,
			PaddedRate:        c.storeCommitRate,
			PaddedBase:        c.storeCommitBase,
			HasMeasured:       c.hasMeasuredIntrinsic,
			MeasuredIntrinsic: c.storeCommitIntrinsic,
			MeasuredEntries:   c.storeCommitCalibratedEntries,
			ChainRate:         c.storeCommitRateRaw,
			ChainBase:         c.storeCommitBaseRaw,
		},
		HardwareDiff: HardwareDiffGas{
			Prev:          hw,
			HasGasPerByte: c.hasHDGasPerByte,
			GasPerByte:    c.hdGasPerByte,
			UnitSize:      c.hdUnitSize,
		},
	}
}

func (c *FeeTreeCache) PriceForMsgs(msgs []sdk.Msg) int64 {
	if c == nil {
		return 0
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	var price uint64
	for _, msg := range msgs {
		if types.IsNetworkDuty(msg) {
			continue
		}
		g := types.FeeGroupOf(msg)
		if g == "" {
			continue
		}
		if _, on := c.enabled[g]; !on {
			continue
		}
		if p := c.groupPrice[g]; p > price {
			price = p
		}
	}
	return int64(price)
}
