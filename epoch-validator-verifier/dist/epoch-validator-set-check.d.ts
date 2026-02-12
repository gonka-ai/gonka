/**
 * Logic module: given epochId (or empty for latest epoch),
 * 1. Get block height of epoch start
 * 2. Get epoch_params at that height with proof; use next block's app_hash to verify proof
 * 3. From epoch_params compute block height where validators should be changed (SetNewValidators)
 * 4. Check that at that block height the new validator set is in effect (validators at setHeight+1 and optionally verify commit)
 */
import { type Validator } from "./block.js";
import { type EpochParamsDecoded } from "./epoch-params.js";
export interface EpochValidatorSetCheckInput {
    /** Epoch ID to check; omit/undefined for latest epoch. */
    epochId?: number;
}
export interface EpochValidatorSetCheckResult {
    /** Epoch ID that was checked. */
    epochId: number;
    /** Block height where this epoch started (PoC start). */
    epochStartHeight: number;
    /** Epoch params fetched at epochStartHeight (with proof). */
    epochParams: EpochParamsDecoded;
    /** Whether the epoch_params Merkle proof was verified against app_hash from block at epochStartHeight+1. */
    paramsProofVerified: boolean;
    /** Block height where validators should be changed (SetNewValidators stage). */
    setNewValidatorsHeight: number;
    /** Validator set at setNewValidatorsHeight (block at this height was produced by previous set). */
    validatorsAtSetHeight: Validator[];
    /** Validator set at setNewValidatorsHeight+1 (should be the new set). */
    validatorsAtSetHeightPlus1: Validator[];
    /** Whether the commit at setNewValidatorsHeight+1 was verified against validatorsAtSetHeightPlus1. */
    commitVerifiedAtSetHeightPlus1: boolean;
    /** True if validator set at setNewValidatorsHeight differs from setNewValidatorsHeight+1. */
    validatorsDiffer: boolean;
    /** Human-readable summary. */
    summary: string;
}
/**
 * Run the full check: resolve epoch, get params with proof, compute set-new-validators height,
 * fetch validators at that height and height+1, and verify the block at height+1 is signed by the new set.
 */
export declare function runEpochValidatorSetCheck(input: EpochValidatorSetCheckInput): Promise<EpochValidatorSetCheckResult>;
