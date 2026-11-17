package types

import (
	"encoding/hex"
	"fmt"
	"strings"

	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
)

// MaxBridgeReissueTargets bounds parameter reads on the request and reissue paths.
const MaxBridgeReissueTargets = 32

func (p *BridgeReissueParams) Validate() error {
	if p == nil {
		return nil // No destination is enabled by default or by the upgrade.
	}
	if len(p.AllowedTargets) > MaxBridgeReissueTargets {
		return fmt.Errorf("at most %d bridge reissue targets are allowed", MaxBridgeReissueTargets)
	}
	seen := make(map[string]struct{}, len(p.AllowedTargets))
	for _, target := range p.AllowedTargets {
		if target.ChainId == "" || len(target.ChainId) > 32 || target.ChainId != strings.ToLower(strings.TrimSpace(target.ChainId)) {
			return fmt.Errorf("bridge reissue chain id must be a canonical lowercase chain name")
		}
		address := target.ContractAddress
		if len(address) != 42 || !strings.HasPrefix(address, "0x") || address != strings.ToLower(address) {
			return fmt.Errorf("bridge reissue contract address must be a lowercase 20-byte hex address")
		}
		decoded, err := hex.DecodeString(address[2:])
		if err != nil || allZeroBytes(decoded) {
			return fmt.Errorf("invalid bridge reissue contract address")
		}
		key := target.ChainId + "/" + address
		if _, exists := seen[key]; exists {
			return fmt.Errorf("duplicate bridge reissue target %s", key)
		}
		seen[key] = struct{}{}
	}
	return nil
}

func allZeroBytes(value []byte) bool {
	for _, b := range value {
		if b != 0 {
			return false
		}
	}
	return true
}

func (msg *MsgReissueBridgeSignature) ValidateBasic() error {
	if _, err := sdk.AccAddressFromBech32(msg.Creator); err != nil {
		return errorsmod.Wrap(sdkerrors.ErrInvalidAddress, "invalid reissue creator")
	}
	// Original request ids include the transaction bytes; do not impose a short
	// hash-only bound that would make existing request-id encoding unusable.
	if msg.RequestId == "" {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "request id cannot be empty")
	}
	return nil
}

func (msg *MsgReissueBridgeSignature) GetSignersStrings() []string {
	return []string{msg.Creator}
}
