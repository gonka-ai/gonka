package v0_2_18

// UpgradeName MUST exactly match the on-chain governance proposal name.
const UpgradeName = "v0.2.18"

// VoteExtensionsEnableDelay is how many blocks after the upgrade height CometBFT
// vote extensions turn on (VoteExtensionsEnableHeight = upgradeHeight + delay).
//
// Enabling is irreversible: once the height is reached it cannot be changed or
// disabled. From that height on every validator's signer must sign vote
// extensions on non-nil precommits (tmkms 0.14.0 with sign_extensions = true; 0.15.0
// drops the extension signature on the wire, iqlusioninc/tmkms#1174).
// A node whose signer does not panics on its first non-nil precommit
// (CometBFT types.SignAndCheckVote: non-recoverable). The delay is a short
// grace window only; the tmkms rollout must be complete BEFORE the upgrade
// proposal passes (see the coordination step in
// proposals/block-bls-beacon/block-bls-beacon-vote-extensions-plan.md).
const VoteExtensionsEnableDelay int64 = 100
