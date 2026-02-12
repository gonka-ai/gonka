/**
 * Logic module: given epochId (or empty for latest epoch),
 * 1. Get block height of epoch start
 * 2. Get epoch_params at that height with proof; use next block's app_hash to verify proof
 * 3. From epoch_params compute block height where validators should be changed (SetNewValidators)
 * 4. Check that at that block height the new validator set is in effect (validators at setHeight+1 and optionally verify commit)
 */
import { getBlock, getCommit, getValidators } from "./block.js";
import { verifyCommitFromValidators } from "./commit-verify.js";
import { getEpochIdAndStartHeight } from "./epoch.js";
import { getEpochParamsAtHeightWithProof, setNewValidatorsHeight, } from "./epoch-params.js";
/** Canonical fingerprint for validator set comparison (address + voting power, sorted by address). */
function validatorSetFingerprint(validators) {
    const parts = [...validators]
        .sort((a, b) => {
        const ah = a.address ? Buffer.from(a.address).toString("hex") : "";
        const bh = b.address ? Buffer.from(b.address).toString("hex") : "";
        return ah.localeCompare(bh);
    })
        .map((v) => {
        const addr = v.address ? Buffer.from(v.address).toString("hex") : "";
        return `${addr}:${v.votingPower.toString()}`;
    });
    return parts.join(",");
}
/**
 * Run the full check: resolve epoch, get params with proof, compute set-new-validators height,
 * fetch validators at that height and height+1, and verify the block at height+1 is signed by the new set.
 */
export async function runEpochValidatorSetCheck(input) {
    const { epochId, startHeight: epochStartHeight } = await getEpochIdAndStartHeight(input.epochId);
    const paramsResult = await getEpochParamsAtHeightWithProof(epochStartHeight);
    const heightWhereValidatorsChange = setNewValidatorsHeight(epochStartHeight, paramsResult.epochParams);
    const [validatorsAtSetHeight, validatorsAtSetHeightPlus1] = await Promise.all([
        getValidators(heightWhereValidatorsChange),
        getValidators(heightWhereValidatorsChange + 1),
    ]);
    const commitAtPlus1 = await getCommit(heightWhereValidatorsChange + 1);
    const blockAtPlus1 = await getBlock(heightWhereValidatorsChange + 1);
    const chainId = blockAtPlus1.block.header.chainId ?? "gonka";
    let commitVerifiedAtSetHeightPlus1 = false;
    if (commitAtPlus1 && validatorsAtSetHeightPlus1.length > 0) {
        const verifyResult = await verifyCommitFromValidators(chainId, commitAtPlus1, heightWhereValidatorsChange + 1, validatorsAtSetHeightPlus1);
        commitVerifiedAtSetHeightPlus1 = verifyResult.ok;
    }
    const fingerprintAtSet = validatorSetFingerprint(validatorsAtSetHeight);
    const fingerprintAtSetPlus1 = validatorSetFingerprint(validatorsAtSetHeightPlus1);
    const validatorsDiffer = fingerprintAtSet !== fingerprintAtSetPlus1;
    const summary = [
        `Epoch ${epochId}: start=${epochStartHeight}, setNewValidators=${heightWhereValidatorsChange}`,
        `Params proof: ${paramsResult.proofVerified ? "OK" : "not verified"}`,
        `Validators at ${heightWhereValidatorsChange}: ${validatorsAtSetHeight.length}, at ${heightWhereValidatorsChange + 1}: ${validatorsAtSetHeightPlus1.length}`,
        `Validators differ (set height vs +1): ${validatorsDiffer ? "yes" : "no"}`,
        `Commit at ${heightWhereValidatorsChange + 1}: ${commitVerifiedAtSetHeightPlus1 ? "OK" : "FAIL"}`,
    ].join("; ");
    return {
        epochId,
        epochStartHeight,
        epochParams: paramsResult.epochParams,
        paramsProofVerified: paramsResult.proofVerified,
        setNewValidatorsHeight: heightWhereValidatorsChange,
        validatorsAtSetHeight,
        validatorsAtSetHeightPlus1,
        commitVerifiedAtSetHeightPlus1,
        validatorsDiffer,
        summary,
    };
}
