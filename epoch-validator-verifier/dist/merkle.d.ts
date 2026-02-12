export interface AbciQueryWithProofResult {
    value: Uint8Array;
    proofOps: ProofOp[] | null;
    height: number;
}
export interface ProofOp {
    type: string;
    key: Uint8Array;
    data: Uint8Array;
}
/**
 * Run ABCI query with prove=true to get value and merkle proof.
 * Uses raw JSON-RPC so proof_ops (snake_case) from the node are read correctly.
 */
export declare function abciQueryWithProof(path: string, data: Uint8Array, height?: number): Promise<AbciQueryWithProofResult>;
/**
 * Verify that the proof commits to the given key/value and matches the expected root (app_hash).
 * Uses ICS23 (Cosmos proof format): supports single IAVL op or multistore (first op = store root, second = IAVL).
 * When DEBUG_VERIFY=1, logs step-by-step verification details.
 */
export declare function verifyProofAgainstRoot(proofOps: ProofOp[], key: Uint8Array, value: Uint8Array, expectedRoot: Uint8Array): boolean;
