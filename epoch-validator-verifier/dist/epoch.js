import { getRpcClient } from "./rpc-client.js";
import { QUERY_EPOCH_INFO_PATH, QUERY_EPOCH_GROUP_DATA_PATH, EPOCH_INFO_REQUEST_B64, API_BASE_URL, EPOCH_GROUP_DATA_REST_PATH, INFERENCE_STORE_PATH, } from "./config.js";
import { abciQueryWithProof } from "./merkle.js";
/** Cosmos SDK REST/gRPC-gateway header for querying at a specific block height. */
export const X_COSMOS_BLOCK_HEIGHT = "x-cosmos-block-height";
/**
 * Fetch EpochGroupData via REST (gRPC-gateway). Regular HTTP GET, no ABCI/JSON-RPC.
 * Use when API_BASE_URL is set (e.g. http://host:1317).
 * Pass blockHeight when the node requires a height in context (e.g. sends x-cosmos-block-height).
 */
export async function getEpochGroupDataRest(epochIndex, modelId, blockHeight) {
    if (!API_BASE_URL)
        return null;
    const base = API_BASE_URL.replace(/\/$/, "");
    const path = `${base}${EPOCH_GROUP_DATA_REST_PATH}/${epochIndex}`;
    const url = modelId ? `${path}?model_id=${encodeURIComponent(modelId)}` : path;
    const headers = { Accept: "application/json" };
    if (blockHeight != null)
        headers[X_COSMOS_BLOCK_HEIGHT] = String(blockHeight);
    const res = await fetch(url, { method: "GET", headers });
    if (!res.ok)
        return null;
    const json = (await res.json());
    const raw = json.epoch_group_data;
    if (!raw)
        return null;
    const validationWeights = (raw.validation_weights ?? []).map((v) => ({
        memberAddress: v.member_address ?? "",
        weight: parseInt(v.weight ?? "0", 10),
    }));
    return {
        pocStartBlockHeight: parseInt(raw.poc_start_block_height ?? "0", 10),
        epochIndex: parseInt(raw.epoch_index ?? "0", 10),
        validationWeights,
    };
}
/** EpochGroupData store key prefix (byte 10). Cosmos SDK collections EpochGroupDataPrefix. */
const EPOCH_GROUP_DATA_PREFIX = 0x0a;
/**
 * Build the inference store key for EpochGroupData as hex (for abci_query data param).
 * Matches inference-chain: collections.Join(epochIndex, modelId) with PairKeyCodec(Uint64Key, StringKey).
 * Key = prefix 0x0A + 8 bytes big-endian epoch_index + string bytes (terminal encoding: no length prefix).
 * For empty model_id "": key = 0x0A + uint64_be(epoch_index) only (9 bytes). Confirmed: 0a000000000000009e returns value.
 */
export function epochGroupDataStoreKeyHex(epochIndex, modelId) {
    const buf = [EPOCH_GROUP_DATA_PREFIX];
    const n = Number(epochIndex);
    if (n < 0 || !Number.isInteger(n) || n > Number.MAX_SAFE_INTEGER)
        throw new Error("epochIndex must be a safe integer");
    // Big-endian 8 bytes: JS bitwise ops are 32-bit, so use division for high 4 bytes
    const hi = Math.floor(n / 0x1_0000_0000) >>> 0;
    const lo = (n >>> 0) >>> 0;
    buf.push((hi >>> 24) & 0xff, (hi >>> 16) & 0xff, (hi >>> 8) & 0xff, hi & 0xff);
    buf.push((lo >>> 24) & 0xff, (lo >>> 16) & 0xff, (lo >>> 8) & 0xff, lo & 0xff);
    const modelBytes = new TextEncoder().encode(modelId);
    for (let i = 0; i < modelBytes.length; i++)
        buf.push(modelBytes[i]);
    // Empty string = 0 bytes (terminal StringKey), no trailing delimiter
    return Buffer.from(buf).toString("hex");
}
/**
 * Decode raw EpochGroupData protobuf bytes (e.g. from store path query).
 * Use this when the value is the raw EpochGroupData, not wrapped in QueryGetEpochGroupDataResponse.
 */
export function decodeEpochGroupDataFromStore(bytes) {
    return decodeEpochGroupData(bytes);
}
/**
 * Request EpochGroupData by store path (/store/inference/key) and key hex.
 * Returns value bytes, proof_ops (if any), height, and decoded EpochGroupData (or null if value empty).
 */
export async function getEpochGroupDataByStoreKey(epochIndex, modelId, height) {
    const keyHex = epochGroupDataStoreKeyHex(epochIndex, modelId);
    const keyBytes = Buffer.from(keyHex, "hex");
    const res = await abciQueryWithProof(INFERENCE_STORE_PATH, keyBytes, height);
    let decoded = null;
    if (res.value.length > 0) {
        try {
            decoded = decodeEpochGroupDataFromStore(res.value);
        }
        catch {
            decoded = null;
        }
    }
    return {
        value: res.value,
        proofOps: res.proofOps,
        height: res.height,
        decoded,
    };
}
/**
 * Get latest epoch ID and the block height where that epoch started (PocStartBlockHeight).
 * Uses ABCI query to inference module EpochInfo.
 */
export async function getLatestEpochInfo() {
    const { client } = await getRpcClient();
    let res;
    try {
        res = await client.abciQuery({
            path: QUERY_EPOCH_INFO_PATH,
            data: Buffer.from(EPOCH_INFO_REQUEST_B64, "base64"),
            prove: false,
        });
    }
    catch (e) {
        const err = e instanceof Error ? e : new Error(String(e));
        const cause = err.cause instanceof Error ? err.cause : null;
        const code = err.code;
        throw new Error(`EpochInfo abci_query failed (path=${QUERY_EPOCH_INFO_PATH}): ${err.message}` +
            (code ? ` code=${code}` : "") +
            (cause ? ` cause=${cause.message}` : ""));
    }
    if (res.code !== 0) {
        throw new Error(`EpochInfo query failed: code=${res.code} log=${res.log}`);
    }
    // Decode QueryEpochInfoResponse (protobuf). We need block_height and latest_epoch { index, poc_start_block_height }.
    const bytes = res.value;
    if (bytes.length === 0) {
        throw new Error("EpochInfo returned empty");
    }
    const decoded = decodeEpochInfoResponse(bytes);
    return {
        blockHeight: decoded.blockHeight,
        latestEpoch: {
            index: Number(decoded.latestEpochIndex),
            pocStartBlockHeight: Number(decoded.pocStartBlockHeight),
        },
    };
}
/**
 * Get block height where a given epoch (by epoch ID) started.
 * Uses ABCI query EpochGroupData(epoch_index, model_id="").
 */
export async function getEpochStartBlockHeight(epochIndex) {
    const { client } = await getRpcClient();
    // QueryGetEpochGroupDataRequest: epoch_index (uint64), model_id (string).
    const requestBytes = encodeEpochGroupDataRequest(epochIndex, "");
    const res = await client.abciQuery({
        path: QUERY_EPOCH_GROUP_DATA_PATH,
        data: requestBytes,
        prove: false,
    });
    if (res.code !== 0) {
        throw new Error(`EpochGroupData query failed for epoch ${epochIndex}: code=${res.code} log=${res.log}`);
    }
    const height = decodeEpochGroupDataPocStartHeight(res.value);
    if (height === 0) {
        throw new Error(`EpochGroupData returned no PocStartBlockHeight for epoch ${epochIndex}`);
    }
    return height;
}
/**
 * Get epoch ID and its start block height. If epochIdArg is provided, use that epoch and
 * fetch its start height; otherwise use the latest epoch from EpochInfo.
 */
export async function getEpochIdAndStartHeight(epochIdArg) {
    if (epochIdArg !== undefined) {
        const startHeight = await getEpochStartBlockHeight(epochIdArg);
        return { epochId: epochIdArg, startHeight };
    }
    const epochInfo = await getLatestEpochInfo();
    return {
        epochId: epochInfo.latestEpoch.index,
        startHeight: epochInfo.latestEpoch.pocStartBlockHeight,
    };
}
// --- Minimal protobuf decoding for our needed fields ---
// QueryEpochInfoResponse: 1=block_height, 2=params, 3=latest_epoch(Epoch), 4=bool, 5=event
// Epoch: 1=index, 2=poc_start_block_height
function decodeEpochInfoResponse(bytes) {
    let offset = 0;
    let blockHeight = 0;
    let latestEpochIndex = 0;
    let pocStartBlockHeight = 0;
    while (offset < bytes.length) {
        const { fieldNum, wireType, consumed } = readTag(bytes, offset);
        offset += consumed;
        if (wireType === 0) {
            const { value, consumed: c } = readVarint(bytes, offset);
            offset += c;
            if (fieldNum === 1)
                blockHeight = Number(value);
        }
        else if (wireType === 2) {
            const { value: len, consumed: lc } = readVarint(bytes, offset);
            offset += lc;
            const sub = bytes.subarray(offset, offset + Number(len));
            offset += Number(len);
            if (fieldNum === 3) {
                const epoch = decodeEpochSubmessage(sub);
                latestEpochIndex = epoch.index;
                pocStartBlockHeight = epoch.pocStartBlockHeight;
            }
        }
    }
    return { blockHeight, latestEpochIndex, pocStartBlockHeight };
}
function decodeEpochSubmessage(bytes) {
    let offset = 0;
    let index = 0;
    let pocStartBlockHeight = 0;
    while (offset < bytes.length) {
        const { fieldNum, wireType, consumed } = readTag(bytes, offset);
        offset += consumed;
        if (wireType === 0) {
            const { value, consumed: c } = readVarint(bytes, offset);
            offset += c;
            if (fieldNum === 1)
                index = Number(value);
            else if (fieldNum === 2)
                pocStartBlockHeight = Number(value);
        }
        else if (wireType === 2) {
            const len = readVarint(bytes, offset).value;
            offset += readVarint(bytes, offset).consumed + Number(len);
        }
    }
    return { index, pocStartBlockHeight };
}
/**
 * Decode QueryGetEpochGroupDataResponse value bytes to EpochGroupData.
 * Response has field 1 = epoch_group_data (embedded EpochGroupData message).
 * EpochGroupData: 1=poc_start_block_height, 8=validation_weights (repeated), 16=epoch_index.
 */
export function decodeQueryGetEpochGroupDataResponse(bytes) {
    const inner = extractEpochGroupDataBytes(bytes);
    if (!inner || inner.length === 0) {
        throw new Error("EpochGroupData response: missing or empty epoch_group_data");
    }
    return decodeEpochGroupData(inner);
}
function extractEpochGroupDataBytes(responseBytes) {
    let offset = 0;
    while (offset < responseBytes.length) {
        const { fieldNum, wireType, consumed } = readTag(responseBytes, offset);
        offset += consumed;
        if (wireType === 2 && fieldNum === 1) {
            const { value: len, consumed: lc } = readVarint(responseBytes, offset);
            offset += lc;
            const end = offset + Number(len);
            return responseBytes.subarray(offset, end);
        }
        if (wireType === 0) {
            const { consumed: c } = readVarint(responseBytes, offset);
            offset += c;
        }
        else if (wireType === 2) {
            const { value: len, consumed: lc } = readVarint(responseBytes, offset);
            offset += lc + Number(len);
        }
    }
    return null;
}
/** Field numbers that may denote validation_weights (repeated) — chain proto may differ. */
const VALIDATION_WEIGHTS_FIELD_NUMS = [2, 3, 4, 5, 8];
function decodeEpochGroupData(bytes) {
    let offset = 0;
    let pocStartBlockHeight = 0;
    let epochIndex = 0;
    const validationWeights = [];
    let field2;
    let field3Bytes;
    while (offset < bytes.length) {
        const { fieldNum, wireType, consumed } = readTag(bytes, offset);
        offset += consumed;
        if (wireType === 0) {
            const { value, consumed: c } = readVarint(bytes, offset);
            offset += c;
            if (fieldNum === 1)
                pocStartBlockHeight = Number(value);
            else if (fieldNum === 2)
                field2 = Number(value);
            else if (fieldNum === 16)
                epochIndex = Number(value);
        }
        else if (wireType === 2) {
            const { value: len, consumed: lc } = readVarint(bytes, offset);
            offset += lc;
            const sub = bytes.subarray(offset, offset + Number(len));
            offset += Number(len);
            if (fieldNum === 3) {
                field3Bytes = sub.slice(0);
            }
            else if (VALIDATION_WEIGHTS_FIELD_NUMS.includes(fieldNum)) {
                const vw = decodeValidationWeight(sub);
                if (vw.memberAddress !== "" || vw.weight !== 0) {
                    validationWeights.push(vw);
                }
            }
        }
    }
    const out = { pocStartBlockHeight, epochIndex, validationWeights };
    if (field2 !== undefined)
        out.field2 = field2;
    if (field3Bytes?.length)
        out.field3Bytes = field3Bytes;
    return out;
}
function decodeValidationWeight(bytes) {
    let offset = 0;
    let memberAddress = "";
    let weight = 0;
    while (offset < bytes.length) {
        const { fieldNum, wireType, consumed } = readTag(bytes, offset);
        offset += consumed;
        if (wireType === 0) {
            const { value, consumed: c } = readVarint(bytes, offset);
            offset += c;
            if (fieldNum === 2)
                weight = Number(value);
        }
        else if (wireType === 2) {
            const { value: len, consumed: lc } = readVarint(bytes, offset);
            offset += lc;
            const sub = bytes.subarray(offset, offset + Number(len));
            offset += Number(len);
            if (fieldNum === 1)
                memberAddress = new TextDecoder().decode(sub);
        }
    }
    return { memberAddress, weight };
}
/**
 * Debug: list field numbers and wire types in the inner EpochGroupData.
 * Use when validationWeights is empty to see what the chain actually returns.
 */
export function debugEpochGroupDataFields(responseBytes) {
    const inner = extractEpochGroupDataBytes(responseBytes);
    if (!inner || inner.length === 0)
        return [];
    const out = [];
    let offset = 0;
    while (offset < inner.length) {
        const { fieldNum, wireType, consumed } = readTag(inner, offset);
        offset += consumed;
        if (wireType === 0) {
            const { consumed: c } = readVarint(inner, offset);
            offset += c;
            out.push({ fieldNum, wireType });
        }
        else if (wireType === 2) {
            const { value: len, consumed: lc } = readVarint(inner, offset);
            offset += lc + Number(len);
            out.push({ fieldNum, wireType, len: Number(len) });
        }
        else {
            out.push({ fieldNum, wireType });
        }
    }
    return out;
}
function decodeEpochGroupDataPocStartHeight(bytes) {
    const inner = extractEpochGroupDataBytes(bytes);
    if (!inner)
        return 0;
    const decoded = decodeEpochGroupData(inner);
    return decoded.pocStartBlockHeight;
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
export function encodeEpochGroupDataRequest(epochIndex, modelId) {
    const parts = [];
    parts.push(encodeVarint(1, BigInt(epochIndex)));
    if (modelId.length > 0) {
        const strBytes = new TextEncoder().encode(modelId);
        parts.push(encodeLengthDelimited(2, strBytes));
    }
    return concatUint8Arrays(parts);
}
function encodeVarint(fieldNum, value) {
    const tag = (fieldNum << 3) | 0;
    const tagBytes = varintEncode(BigInt(tag));
    const valueBytes = varintEncode(value);
    const out = new Uint8Array(tagBytes.length + valueBytes.length);
    out.set(tagBytes, 0);
    out.set(valueBytes, tagBytes.length);
    return out;
}
function encodeLengthDelimited(fieldNum, data) {
    const tag = (fieldNum << 3) | 2;
    const tagBytes = varintEncode(BigInt(tag));
    const lenBytes = varintEncode(BigInt(data.length));
    const out = new Uint8Array(tagBytes.length + lenBytes.length + data.length);
    let o = 0;
    out.set(tagBytes, o);
    o += tagBytes.length;
    out.set(lenBytes, o);
    o += lenBytes.length;
    out.set(data, o);
    return out;
}
function varintEncode(n) {
    const bytes = [];
    let x = n;
    while (x > BigInt(0x7f)) {
        bytes.push(Number((x & BigInt(0x7f)) | BigInt(0x80)));
        x >>= BigInt(7);
    }
    bytes.push(Number(x));
    return new Uint8Array(bytes);
}
function concatUint8Arrays(arr) {
    const total = arr.reduce((s, a) => s + a.length, 0);
    const out = new Uint8Array(total);
    let offset = 0;
    for (const a of arr) {
        out.set(a, offset);
        offset += a.length;
    }
    return out;
}
