package v0_2_13

// UpgradeName MUST exactly match the on-chain governance proposal name.
// Cosmovisor matches the announced name to this string when scheduling
// the binary swap; any drift between proposal text and this constant
// silently bypasses the upgrade handler.
const UpgradeName = "v0.2.13"
