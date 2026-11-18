package state

import (
	"fmt"

	"devshard/signing"
	"devshard/types"
)

// VerifyDiffUserSig recovers the Diff user signature and requires it to be
// the escrow creator. Used by apply and by gateway start-proof checks before
// CreateSession.
func VerifyDiffUserSig(verifier signing.Verifier, creatorAddr, escrowID string, diff types.Diff) error {
	if verifier == nil {
		return fmt.Errorf("%w: verifier is nil", types.ErrInvalidUserSig)
	}
	diffContent := BuildDiffContent(escrowID, diff.Nonce, diff.Txs, diff.PostStateRoot)
	data, err := deterministicMarshal.Marshal(diffContent)
	if err != nil {
		return fmt.Errorf("marshal diff content: %w", err)
	}
	recovered, err := verifier.RecoverAddress(data, diff.UserSig)
	if err != nil {
		return fmt.Errorf("%w: %v", types.ErrInvalidUserSig, err)
	}
	if recovered != creatorAddr {
		return fmt.Errorf("%w: expected %s, got %s", types.ErrInvalidUserSig, creatorAddr, recovered)
	}
	return nil
}

// GatewayStartVersion returns the protocol_version from a creator-signed
// MsgStartInference in diffs. Every diff must verify as the creator. At least
// one StartInference must carry a non-empty protocol_version; conflicting
// versions fail.
func GatewayStartVersion(verifier signing.Verifier, creatorAddr, escrowID string, diffs []types.Diff) (string, error) {
	if len(diffs) == 0 {
		return "", types.ErrStartProofMissing
	}
	for i, diff := range diffs {
		if err := VerifyDiffUserSig(verifier, creatorAddr, escrowID, diff); err != nil {
			return "", fmt.Errorf("diff %d: %w", i, err)
		}
	}
	return types.StartProofVersion(diffs)
}
