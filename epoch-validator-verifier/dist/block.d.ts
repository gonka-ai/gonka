/** Header may include validatorsHash / nextValidatorsHash from RPC (for proof verification). */
export interface BlockHeaderWithProof {
    height: bigint;
    appHash: Uint8Array;
    chainId: string;
    validatorsHash?: Uint8Array;
    nextValidatorsHash?: Uint8Array;
}
/** Raw header from RPC may use snake_case and base64 (e.g. validators_hash). */
type HeaderLike = BlockHeaderWithProof & {
    validators_hash?: string;
};
/**
 * Get validators_hash from a block header (block validators, not participants).
 * Supports both validatorsHash (Uint8Array) and validators_hash (base64 string) from RPC.
 */
export declare function getValidatorsHashFromHeader(header: HeaderLike): Uint8Array | undefined;
export interface BlockWithCommit {
    block: {
        header: BlockHeaderWithProof;
        lastCommit: {
            height: bigint;
            blockId: {
                hash: Uint8Array;
            };
            signatures: Array<{
                validatorId?: string;
                signature?: Uint8Array;
            }>;
        } | null;
    };
    commit: {
        height: bigint;
        blockId: {
            hash: Uint8Array;
        };
        signatures: Array<{
            validatorId?: string;
            signature?: Uint8Array;
        }>;
    } | null;
}
export interface Validator {
    pubkey: {
        typeUrl?: string;
        value?: Uint8Array;
    };
    votingPower: bigint;
    address?: Uint8Array;
}
/**
 * Get block at height. The block includes LastCommit (signatures for the previous block).
 */
export declare function getBlock(height: number): Promise<BlockWithCommit>;
/**
 * Get commit for a specific height (optional; commit is also in block at height+1 as lastCommit).
 */
export declare function getCommit(height: number): Promise<BlockWithCommit["commit"]>;
/**
 * Get full validator set at a given height (consensus validators who signed that height).
 * Paginates until a page returns fewer than per_page items. If the node only allows page 1
 * (e.g. "page should be within [1, 1]"), we use the validators from the first page.
 */
export declare function getValidators(height: number): Promise<Validator[]>;
export interface ValidatorsWithProofContext {
    validators: Validator[];
    block: BlockWithCommit;
    /** Hash of this validator set as committed in the block header (Tendermint consensus proof). */
    validatorsHashFromHeader: Uint8Array | undefined;
}
/**
 * Get validator set at a given height together with the block header's validators_hash.
 * Use this when you need a "proof" that the validator set is the one committed in the chain:
 * - The block at height H has header.validators_hash = hash(validator set at H).
 * - The commit (signed by validators at H-1) attests to that header.
 * Full verification: recompute Tendermint's ValidatorSet hash (proto encoding + SHA256) and
 * compare to validatorsHashFromHeader; see docs/validators-with-proof.md.
 */
export declare function getValidatorsWithProofContext(height: number): Promise<ValidatorsWithProofContext>;
/**
 * Scan blocks starting at fromHeight and return the first height where the consensus
 * validator set (block validators) changes. Uses block header validators_hash when
 * available; otherwise compares validator lists from /validators.
 * @param fromHeight – start scanning from this block (checks fromHeight+1, +2, …).
 * @param maxBlocks – optional cap (default 10000); stop after this many blocks if no change.
 * @returns The first height > fromHeight where validators differ from the previous block, or null if no change found within the limit.
 */
export declare function findHeightWhereValidatorsChange(fromHeight: number, maxBlocks?: number): Promise<number | null>;
export {};
