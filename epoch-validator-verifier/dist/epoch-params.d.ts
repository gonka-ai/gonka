/**
 * Fetch inference module EpochParams at a given height with Merkle proof,
 * verify proof against app_hash from the next block, and compute the block height
 * where validators should be changed (SetNewValidators stage).
 *
 * Matches inference-chain: GetSetNewValidatorsStage() = GetEndOfPoCValidationStage() + SetNewValidatorsDelay,
 * with GetEndOfPoCValidationStage() = PocStageDuration + PocValidationDelay + PocValidationDuration.
 */
import { type ProofOp } from "./merkle.js";
export interface EpochParamsDecoded {
    pocStageDuration: number;
    pocValidationDelay: number;
    pocValidationDuration: number;
    setNewValidatorsDelay: number;
}
export interface EpochParamsWithProofResult {
    /** Decoded epoch params (subset needed for set-new-validators height). */
    epochParams: EpochParamsDecoded;
    /** Raw value bytes from store. */
    value: Uint8Array;
    /** Proof ops from abci_query (prove=true). */
    proofOps: ProofOp[] | null;
    /** Height at which the query was executed. */
    queryHeight: number;
    /** Block used for app_hash verification (queryHeight + 1). */
    appHashBlockHeight: number;
    /** Whether the Merkle proof was verified against app_hash. */
    proofVerified: boolean;
}
/**
 * Compute block height where validators should be changed (SetNewValidators stage).
 * setNewValidatorsHeight = epochStart + PocStageDuration + PocValidationDelay + PocValidationDuration + SetNewValidatorsDelay
 */
export declare function setNewValidatorsHeight(epochStartHeight: number, params: EpochParamsDecoded): number;
/**
 * Fetch inference module params at the given height with prove=true.
 * Verifies the proof against app_hash from block at height+1.
 * Decodes EpochParams (poc_stage_duration, poc_validation_delay, poc_validation_duration, set_new_validators_delay).
 */
export declare function getEpochParamsAtHeightWithProof(height: number): Promise<EpochParamsWithProofResult>;
