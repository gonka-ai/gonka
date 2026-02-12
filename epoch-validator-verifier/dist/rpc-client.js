import { Tendermint34Client } from "@cosmjs/tendermint-rpc";
import { RPC_NODES } from "./config.js";
let currentClient = null;
let currentUrl = null;
/**
 * Try to connect to the first available RPC node from the list.
 * Uses failover: tries each URL in order until one connects.
 */
export async function getRpcClient() {
    if (currentClient != null && currentUrl != null) {
        return { client: currentClient, url: currentUrl };
    }
    const errors = [];
    for (const url of RPC_NODES) {
        try {
            const client = await Tendermint34Client.connect(url);
            const status = await client.status();
            if (status?.syncInfo) {
                currentClient = client;
                currentUrl = url;
                console.log("RPC connected:", url);
                return { client, url };
            }
        }
        catch (e) {
            const err = e instanceof Error ? e : new Error(String(e));
            const cause = err.cause instanceof Error ? err.cause : null;
            const code = err.code ?? cause?.code;
            const detail = [
                err.message,
                code ? `code=${code}` : "",
                cause ? `cause=${cause.message}` : "",
            ]
                .filter(Boolean)
                .join(" ");
            errors.push({ url, err, detail });
            console.warn("RPC failed for", url, "—", detail);
            if (cause)
                console.warn("  cause:", cause.message);
        }
    }
    const summary = errors.map(({ url, detail }) => `${url}: ${detail}`).join("; ");
    throw new Error("All RPC nodes failed. " +
        summary +
        ". Check: RPC reachable (curl " +
        RPC_NODES[0] +
        "/status), firewall, and RPC_URL_1 env.");
}
/**
 * Reset the cached client (e.g. after a connection error to retry with failover).
 */
export function resetRpcClient() {
    if (currentClient != null) {
        currentClient.disconnect();
        currentClient = null;
        currentUrl = null;
    }
}
