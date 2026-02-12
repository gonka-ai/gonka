import { Tendermint34Client } from "@cosmjs/tendermint-rpc";
/**
 * Try to connect to the first available RPC node from the list.
 * Uses failover: tries each URL in order until one connects.
 */
export declare function getRpcClient(): Promise<{
    client: Tendermint34Client;
    url: string;
}>;
/**
 * Reset the cached client (e.g. after a connection error to retry with failover).
 */
export declare function resetRpcClient(): void;
