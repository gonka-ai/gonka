import { type ProofOp } from "./merkle.js";
/** Decoded Epoch from chain (inference module). */
export interface Epoch {
    index: number;
    pocStartBlockHeight: number;
}
/** EpochInfo response: latest epoch and block height. */
export interface EpochInfoResult {
    blockHeight: number;
    latestEpoch: Epoch;
}
/** ValidationWeight from EpochGroupData (participant/validator with weight). */
export interface ValidationWeightDecoded {
    memberAddress: string;
    weight: number;
}
/** Decoded EpochGroupData from QueryGetEpochGroupDataResponse (epoch_group_data field). */
export interface EpochGroupDataDecoded {
    pocStartBlockHeight: number;
    epochIndex: number;
    validationWeights: ValidationWeightDecoded[];
    /** Chain-specific: second varint (field 2) if present. */
    field2?: number;
    /** Chain-specific: single length-delimited bytes (e.g. field 3, 64 bytes) if present. */
    field3Bytes?: Uint8Array;
}
/** REST response shape for EpochGroupData (gRPC-gateway returns snake_case JSON). */
export interface EpochGroupDataRestResponse {
    epoch_group_data?: {
        poc_start_block_height?: string;
        epoch_index?: string;
        epoch_group_id?: string;
        epoch_policy?: string;
        validation_weights?: Array<{
            member_address?: string;
            weight?: string;
        }>;
    };
}
/** Cosmos SDK REST/gRPC-gateway header for querying at a specific block height. */
export declare const X_COSMOS_BLOCK_HEIGHT = "x-cosmos-block-height";
/**
 * Fetch EpochGroupData via REST (gRPC-gateway). Regular HTTP GET, no ABCI/JSON-RPC.
 * Use when API_BASE_URL is set (e.g. http://host:1317).
 * Pass blockHeight when the node requires a height in context (e.g. sends x-cosmos-block-height).
 */
export declare function getEpochGroupDataRest(epochIndex: number, modelId: string, blockHeight?: number): Promise<EpochGroupDataDecoded | null>;
/**
 * Build the inference store key for EpochGroupData as hex (for abci_query data param).
 * Matches inference-chain: collections.Join(epochIndex, modelId) with PairKeyCodec(Uint64Key, StringKey).
 * Key = prefix 0x0A + 8 bytes big-endian epoch_index + string bytes (terminal encoding: no length prefix).
 * For empty model_id "": key = 0x0A + uint64_be(epoch_index) only (9 bytes). Confirmed: 0a000000000000009e returns value.
 */
export declare function epochGroupDataStoreKeyHex(epochIndex: number, modelId: string): string;
/**
 * Decode raw EpochGroupData protobuf bytes (e.g. from store path query).
 * Use this when the value is the raw EpochGroupData, not wrapped in QueryGetEpochGroupDataResponse.
 */
export declare function decodeEpochGroupDataFromStore(bytes: Uint8Array): EpochGroupDataDecoded;
export interface EpochGroupDataByStoreKeyResult {
    value: Uint8Array;
    proofOps: ProofOp[] | null;
    height: number;
    decoded: EpochGroupDataDecoded | null;
}
/**
 * Request EpochGroupData by store path (/store/inference/key) and key hex.
 * Returns value bytes, proof_ops (if any), height, and decoded EpochGroupData (or null if value empty).
 */
export declare function getEpochGroupDataByStoreKey(epochIndex: number, modelId: string, height?: number): Promise<EpochGroupDataByStoreKeyResult>;
/**
 * Get latest epoch ID and the block height where that epoch started (PocStartBlockHeight).
 * Uses ABCI query to inference module EpochInfo.
 */
export declare function getLatestEpochInfo(): Promise<EpochInfoResult>;
/**
 * Get block height where a given epoch (by epoch ID) started.
 * Uses ABCI query EpochGroupData(epoch_index, model_id="").
 */
export declare function getEpochStartBlockHeight(epochIndex: number): Promise<number>;
/**
 * Get epoch ID and its start block height. If epochIdArg is provided, use that epoch and
 * fetch its start height; otherwise use the latest epoch from EpochInfo.
 */
export declare function getEpochIdAndStartHeight(epochIdArg?: number): Promise<{
    epochId: number;
    startHeight: number;
}>;
/**
 * Decode QueryGetEpochGroupDataResponse value bytes to EpochGroupData.
 * Response has field 1 = epoch_group_data (embedded EpochGroupData message).
 * EpochGroupData: 1=poc_start_block_height, 8=validation_weights (repeated), 16=epoch_index.
 */
export declare function decodeQueryGetEpochGroupDataResponse(bytes: Uint8Array): EpochGroupDataDecoded;
/**
 * Debug: list field numbers and wire types in the inner EpochGroupData.
 * Use when validationWeights is empty to see what the chain actually returns.
 */
export declare function debugEpochGroupDataFields(responseBytes: Uint8Array): Array<{
    fieldNum: number;
    wireType: number;
    len?: number;
}>;
export declare function encodeEpochGroupDataRequest(epochIndex: number, modelId: string): Uint8Array;
