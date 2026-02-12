/**
 * Configuration: list of RPC node URLs. The client tries them in order until one succeeds.
 */
export declare const RPC_NODES: string[];
/**
 * REST API base URL (gRPC-gateway). If set, EpochGroupData can be fetched via HTTP GET
 * instead of ABCI query. Typical: same host as RPC but port 1317 (e.g. http://host:1317).
 */
export declare const API_BASE_URL: string;
/** ABCI query path for inference module EpochInfo (latest epoch). gRPC full method name from proto package inference.inference. */
export declare const QUERY_EPOCH_INFO_PATH = "/inference.inference.Query/EpochInfo";
/** ABCI query path for EpochGroupData by epoch index (returns PocStartBlockHeight). */
export declare const QUERY_EPOCH_GROUP_DATA_PATH = "/inference.inference.Query/EpochGroupData";
/** REST path for EpochGroupData (gRPC-gateway). GET {API_BASE_URL}{EPOCH_GROUP_DATA_REST_PATH}/{epoch_index}?model_id= */
export declare const EPOCH_GROUP_DATA_REST_PATH = "/productscience/inference/inference/epoch_group_data";
/** ABCI store path for inference module (raw key-value with optional proof). */
export declare const INFERENCE_STORE_PATH = "/store/inference/key";
/** Inference module params store key (keeper ParamsKey). Used for abci_query with proof. */
export declare const INFERENCE_PARAMS_KEY = "p_inference";
/** Empty EpochInfo request (base64). */
export declare const EPOCH_INFO_REQUEST_B64: string;
