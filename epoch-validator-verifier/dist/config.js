/**
 * Configuration: list of RPC node URLs. The client tries them in order until one succeeds.
 */
export const RPC_NODES = [
    process.env.RPC_URL_1 ?? "http://185.92.223.230:26657"
].filter(Boolean);
/**
 * REST API base URL (gRPC-gateway). If set, EpochGroupData can be fetched via HTTP GET
 * instead of ABCI query. Typical: same host as RPC but port 1317 (e.g. http://host:1317).
 */
export const API_BASE_URL = process.env.API_BASE_URL ?? "";
/** ABCI query path for inference module EpochInfo (latest epoch). gRPC full method name from proto package inference.inference. */
export const QUERY_EPOCH_INFO_PATH = "/inference.inference.Query/EpochInfo";
/** ABCI query path for EpochGroupData by epoch index (returns PocStartBlockHeight). */
export const QUERY_EPOCH_GROUP_DATA_PATH = "/inference.inference.Query/EpochGroupData";
/** REST path for EpochGroupData (gRPC-gateway). GET {API_BASE_URL}{EPOCH_GROUP_DATA_REST_PATH}/{epoch_index}?model_id= */
export const EPOCH_GROUP_DATA_REST_PATH = "/productscience/inference/inference/epoch_group_data";
/** ABCI store path for inference module (raw key-value with optional proof). */
export const INFERENCE_STORE_PATH = "/store/inference/key";
/** Inference module params store key (keeper ParamsKey). Used for abci_query with proof. */
export const INFERENCE_PARAMS_KEY = "p_inference";
/** Empty EpochInfo request (base64). */
export const EPOCH_INFO_REQUEST_B64 = Buffer.from(new Uint8Array(0)).toString("base64");
