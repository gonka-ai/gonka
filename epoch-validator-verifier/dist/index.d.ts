/**
 * Epoch Validator Verifier
 *
 * 1. Connects to RPC nodes (failover list).
 * 2. Gets latest epoch ID and block height where that epoch started.
 * 3. Gets block at new epoch start; gets validators with merkle proof (ABCI query prove=true).
 * 4. Does the same for the previous epoch.
 * 5. Verifies that the new epoch block's LastCommit signatures are from the previous epoch's validators.
 */
import "dotenv/config";
export { runEpochValidatorSetCheck, type EpochValidatorSetCheckInput, type EpochValidatorSetCheckResult, } from "./epoch-validator-set-check.js";
export { getEpochParamsAtHeightWithProof, setNewValidatorsHeight, type EpochParamsDecoded, type EpochParamsWithProofResult, } from "./epoch-params.js";
