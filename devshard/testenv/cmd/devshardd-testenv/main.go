// Binary devshardd-testenv is the testenv-specific devshardd host
// process: hex-key signer, mock-dapi library, stub inference/validation
// engines, MainnetBridge backed by the mock-chain.
//
// Phase 8; see devshard/docs/testenv.md §Phase 8.
package main

func main() {
	// TODO(phase-8): wire HostManager per §5 and §Phase 8:
	//   md := mockdapi.New(ctx, mockdapi.Config{...})
	//   devshard.NewHostManager(devshard.HostManagerConfig{
	//       Signer:           signing.MustSignerFromHex(os.Getenv("TESTENV_PRIVATE_KEY")),
	//       Bridge:           testenvbridge.NewGRPCBridge(ctx, mockChainURL),
	//       BlockOracle:      md.Oracle,
	//       NodeManager:      md.NodeManager,
	//       InferenceEngine:  testenvengine.NewMockInference(cfg),
	//       ValidationEngine: testenvengine.NewMockValidation(cfg),
	//       Storage:          mustOpenSQLite(dataDir),
	//   })
}
