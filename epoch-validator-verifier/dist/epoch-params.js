/**
 * Fetch inference module EpochParams at a given height with Merkle proof,
 * verify proof against app_hash from the next block, and compute the block height
 * where validators should be changed (SetNewValidators stage).
 *
 * Matches inference-chain: GetSetNewValidatorsStage() = GetEndOfPoCValidationStage() + SetNewValidatorsDelay,
 * with GetEndOfPoCValidationStage() = PocStageDuration + PocValidationDelay + PocValidationDuration.
 */
import { getBlock } from "./block.js";
import { INFERENCE_STORE_PATH, INFERENCE_PARAMS_KEY } from "./config.js";
import { abciQueryWithProof } from "./merkle.js";
import { verifyProofAgainstRoot } from "./merkle.js";
/**
 * Compute block height where validators should be changed (SetNewValidators stage).
 * setNewValidatorsHeight = epochStart + PocStageDuration + PocValidationDelay + PocValidationDuration + SetNewValidatorsDelay
 */
export function setNewValidatorsHeight(epochStartHeight, params) {
    return (epochStartHeight +
        params.pocStageDuration +
        params.pocValidationDelay +
        params.pocValidationDuration +
        params.setNewValidatorsDelay);
}
const PARAMS_KEY_BYTES = new TextEncoder().encode(INFERENCE_PARAMS_KEY);
/**
 * Fetch inference module params at the given height with prove=true.
 * Verifies the proof against app_hash from block at height+1.
 * Decodes EpochParams (poc_stage_duration, poc_validation_delay, poc_validation_duration, set_new_validators_delay).
 */
export async function getEpochParamsAtHeightWithProof(height) {
    const res = await abciQueryWithProof(INFERENCE_STORE_PATH, PARAMS_KEY_BYTES, height);
    const epochParams = decodeEpochParamsFromParamsValue(res.value);
    const appHashBlockHeight = res.height + 1;
    const appHashBlock = await getBlock(appHashBlockHeight);
    const appHash = appHashBlock.block.header.appHash;
    const rawHeader = appHashBlock.block.header;
    const appHashBytes = appHash ??
        (rawHeader.app_hash != null ? Uint8Array.from(Buffer.from(rawHeader.app_hash, "base64")) : null);
    let proofVerified = false;
    if (appHashBytes && appHashBytes.length > 0 && res.proofOps && res.proofOps.length > 0) {
        proofVerified = verifyProofAgainstRoot(res.proofOps, PARAMS_KEY_BYTES, res.value, appHashBytes);
    }
    return {
        epochParams,
        value: res.value,
        proofOps: res.proofOps,
        queryHeight: res.height,
        appHashBlockHeight,
        proofVerified,
    };
}
/**
 * Decode Params protobuf and extract EpochParams fields we need.
 * Params: field 1 = epoch_params (EpochParams message).
 * EpochParams: 5=poc_stage_duration, 6=poc_exchange_duration, 7=poc_validation_delay, 8=poc_validation_duration, 9=set_new_validators_delay (all int64 varint).
 */
function decodeEpochParamsFromParamsValue(paramsValue) {
    const epochParamsBytes = extractFieldLengthDelimited(paramsValue, 1);
    if (!epochParamsBytes || epochParamsBytes.length === 0) {
        throw new Error("Params value: missing or empty epoch_params (field 1)");
    }
    let pocStageDuration = 10;
    let pocValidationDelay = 2;
    let pocValidationDuration = 6;
    let setNewValidatorsDelay = 1;
    let offset = 0;
    while (offset < epochParamsBytes.length) {
        const { fieldNum, wireType, consumed } = readTag(epochParamsBytes, offset);
        offset += consumed;
        if (wireType === 0) {
            const { value, consumed: c } = readVarint(epochParamsBytes, offset);
            offset += c;
            const v = Number(value);
            if (fieldNum === 5)
                pocStageDuration = v;
            else if (fieldNum === 7)
                pocValidationDelay = v;
            else if (fieldNum === 8)
                pocValidationDuration = v;
            else if (fieldNum === 9)
                setNewValidatorsDelay = v;
        }
        else if (wireType === 2) {
            const { value: len, consumed: lc } = readVarint(epochParamsBytes, offset);
            offset += lc + Number(len);
        }
    }
    return {
        pocStageDuration,
        pocValidationDelay,
        pocValidationDuration,
        setNewValidatorsDelay,
    };
}
function extractFieldLengthDelimited(buf, fieldNum) {
    let offset = 0;
    while (offset < buf.length) {
        const { fieldNum: fn, wireType, consumed } = readTag(buf, offset);
        offset += consumed;
        if (wireType === 2 && fn === fieldNum) {
            const { value: len, consumed: lc } = readVarint(buf, offset);
            offset += lc;
            return buf.subarray(offset, offset + Number(len));
        }
        if (wireType === 0) {
            const { consumed: c } = readVarint(buf, offset);
            offset += c;
        }
        else if (wireType === 2) {
            const { value: len, consumed: lc } = readVarint(buf, offset);
            offset += lc + Number(len);
        }
    }
    return null;
}
function readTag(buf, offset) {
    const { value, consumed } = readVarint(buf, offset);
    const wireType = Number(value) & 0x7;
    const fieldNum = Number(value) >>> 3;
    return { fieldNum, wireType, consumed };
}
function readVarint(buf, offset) {
    let value = BigInt(0);
    let shift = 0;
    let consumed = 0;
    while (offset + consumed < buf.length) {
        const b = buf[offset + consumed];
        consumed += 1;
        value |= BigInt(b & 0x7f) << BigInt(shift);
        if ((b & 0x80) === 0)
            break;
        shift += 7;
    }
    return { value, consumed };
}
