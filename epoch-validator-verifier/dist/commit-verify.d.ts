import type { BlockWithCommit, Validator } from "./block.js";
export interface VerifyCommitResult {
    ok: boolean;
    signedPower: bigint;
    totalPower: bigint;
    errors: string[];
    /** Consensus addresses (hex, 40 chars) of validators that signed the commit (crypto-verified). */
    signerAddresses: string[];
    /** Consensus addresses (hex) from commit signatures with non-empty signature (before crypto verify). Use to check "who claimed to sign" when verification fails. */
    commitSignerAddresses: string[];
}
export interface VerifyCommitOptions {
    /** When true, log sign bytes (hex) and verification for first N signatures (for comparison with Go -debug). */
    debug?: boolean;
}
export declare function verifyCommitFromValidators(chainId: string, commit: NonNullable<BlockWithCommit["commit"]>, blockHeight: number, validators: Validator[], opts?: VerifyCommitOptions): Promise<VerifyCommitResult>;
/** Normalize consensus address to 40-char hex (uppercase) for comparison. */
export declare function consensusAddressToHex(addr: string): string;
/** Debug: encoding inputs used for CanonicalVote (for byte-by-byte comparison with Go). */
export interface EncodeVoteSignBytesDebugInfo {
    height: bigint;
    round: bigint;
    blockIdHash: Uint8Array;
    partsTotal: number;
    partsHash: Uint8Array;
    seconds: number | bigint;
    nanos: number;
    signBytes: Uint8Array;
}
