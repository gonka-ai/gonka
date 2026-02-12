import { toSeconds } from "@cosmjs/tendermint-rpc";
import * as ed from "@noble/ed25519";
/**
 * Verify that the commit (block signatures) was produced by the given validator set.
 * CometBFT commits are signed with Ed25519; we check that > 2/3 of voting power signed.
 */
const DEBUG_LOG_FIRST_N = 3;
export async function verifyCommitFromValidators(chainId, commit, blockHeight, validators, opts) {
    const errors = [];
    const signerAddresses = [];
    const commitSignerAddresses = [];
    let signedPower = BigInt(0);
    let totalPower = BigInt(0);
    const debug = opts?.debug ?? false;
    let debugLogged = 0;
    const validatorById = new Map();
    for (let i = 0; i < validators.length; i++) {
        const v = validators[i];
        validatorById.set(i, v);
        if (v.address?.length)
            validatorById.set(validatorAddressToId(v.address), v);
        totalPower += v.votingPower;
    }
    if (totalPower === BigInt(0)) {
        return { ok: false, signedPower: BigInt(0), totalPower: BigInt(0), errors: ["empty validator set"], signerAddresses: [], commitSignerAddresses: [] };
    }
    const validatorsList = [...validators];
    for (let i = 0; i < commit.signatures.length; i++) {
        const sig = commit.signatures[i];
        if (!sig.signature || sig.signature.length === 0)
            continue;
        const raw = sig;
        if (raw.validatorAddress?.length) {
            commitSignerAddresses.push(validatorAddressToId(raw.validatorAddress));
        }
        // Prefer validator_address from commit (CosmJS decoded); then validatorIndex; then index i.
        const validator = (raw.validatorAddress?.length
            ? validatorById.get(validatorAddressToId(raw.validatorAddress))
            : null) ??
            (typeof raw.validatorIndex === "number" && validatorsList[raw.validatorIndex]
                ? validatorsList[raw.validatorIndex]
                : null) ??
            validatorById.get(raw.validatorId) ??
            (validatorsList[i] ?? null);
        const validatorId = raw.validatorId ?? raw.validatorIndex;
        if (!validator) {
            errors.push(`Unknown validator in commit: ${validatorId ?? raw.validatorAddress ?? i}`);
            continue;
        }
        // signBytes = MarshalDelimited(CanonicalVote). Per CometBFT: each validator signs their own vote;
        // BlockID in that vote is commit.BlockID for COMMIT (2), empty for NIL (3) / ABSENT (1).
        if (debug && debugLogged === 0) {
            console.error("\n--- TS debug: CanonicalVote sign bytes and verification ---");
            const c = commit;
            const hashHex = c.blockId?.hash?.length ? bufferToHex(c.blockId.hash) : "";
            const partsTotal = c.blockId?.parts?.total ?? 0;
            const partsHashHex = c.blockId?.parts?.hash?.length ? bufferToHex(c.blockId.parts.hash) : "";
            console.error(`Commit height=${commit.height} round=${commit.round ?? 0} block_id.hash=${hashHex}`);
            if (partsTotal > 0)
                console.error(`Commit block_id.parts: total=${partsTotal} hash=${partsHashHex}`);
            console.error(`Chain ID: ${JSON.stringify(chainId)}\n`);
        }
        const signBytes = encodeVoteSignBytes(chainId, commit, sig, 2, debug && debugLogged < DEBUG_LOG_FIRST_N ? (info) => logEncodeDebug(i, raw.validatorAddress, info) : undefined);
        const idLabel = validatorId !== undefined && validatorId !== null
            ? String(validatorId)
            : validator.address?.length
                ? validatorAddressToId(validator.address)
                : `index ${i}`;
        let valid = false;
        try {
            const pubkey = extractEd25519Pubkey(validator.pubkey);
            if (!pubkey) {
                errors.push(`Validator ${idLabel}: not Ed25519 pubkey`);
                if (debug && debugLogged < DEBUG_LOG_FIRST_N) {
                    logVerifyDebug(i, raw.validatorAddress, signBytes, sig.signature, false);
                    debugLogged++;
                }
                continue;
            }
            valid = await ed.verifyAsync(sig.signature, signBytes, pubkey);
            if (valid) {
                signedPower += validator.votingPower;
                if (validator.address?.length) {
                    signerAddresses.push(validatorAddressToId(validator.address));
                }
            }
            else {
                errors.push(`Validator ${idLabel}: invalid signature`);
            }
            if (debug && debugLogged < DEBUG_LOG_FIRST_N) {
                logVerifyDebug(i, raw.validatorAddress, signBytes, sig.signature, valid);
                debugLogged++;
            }
        }
        catch (e) {
            errors.push(`Validator ${idLabel}: ${e.message}`);
            if (debug && debugLogged < DEBUG_LOG_FIRST_N) {
                logVerifyDebug(i, raw.validatorAddress, signBytes, sig.signature, false);
                debugLogged++;
            }
        }
    }
    const twoThirds = (totalPower * BigInt(2) + BigInt(2)) / BigInt(3);
    const ok = signedPower >= twoThirds;
    return { ok, signedPower, totalPower, errors, signerAddresses, commitSignerAddresses };
}
/** Normalize consensus address to 40-char hex (uppercase) for comparison. */
export function consensusAddressToHex(addr) {
    const hex = addr.replace(/^0x/i, "").replace(/\s/g, "");
    if (hex.length >= 40)
        return hex.slice(0, 40).toUpperCase();
    return hex.toUpperCase();
}
function validatorAddressToId(address) {
    return Buffer.from(address).toString("hex").toUpperCase().slice(0, 40);
}
function bufferToHex(b) {
    return Buffer.from(b).toString("hex");
}
function logEncodeDebug(index, validatorAddress, info) {
    const addr = validatorAddress?.length ? bufferToHex(validatorAddress).toUpperCase().slice(0, 40) : "";
    console.error(`[${index}] validator_address=${addr}`);
    console.error(`     vote: type=2 height=${info.height} round=${info.round} block_id.hash=${bufferToHex(info.blockIdHash)} block_id.parts.total=${info.partsTotal}`);
    console.error(`     vote.timestamp: seconds=${String(info.seconds)} nanos=${info.nanos}`);
    console.error(`     sign_bytes_hex (${info.signBytes.length} bytes): ${bufferToHex(info.signBytes)}`);
}
function logVerifyDebug(index, validatorAddress, signBytes, signature, ok) {
    const addr = validatorAddress?.length ? bufferToHex(validatorAddress).toUpperCase().slice(0, 40) : "";
    console.error(`     signature_hex (${signature.length} bytes): ${bufferToHex(signature)}`);
    console.error(`     verify: ${ok}`);
}
function extractEd25519Pubkey(pubkey) {
    // CosmJS ValidatorPubkey: { algorithm: "ed25519", data: Uint8Array }
    const byData = pubkey.data;
    if (byData != null && byData.length === 32) {
        const alg = pubkey.algorithm ?? "";
        if (alg.toLowerCase().includes("ed25519"))
            return byData;
        return byData;
    }
    let val = pubkey?.value;
    if (val == null)
        return null;
    if (typeof val === "string") {
        try {
            val = Uint8Array.from(Buffer.from(val, "base64"));
        }
        catch {
            return null;
        }
    }
    if (val.length !== 32)
        return null;
    const typeUrl = pubkey.typeUrl ?? pubkey.type_url ?? "";
    if (typeUrl && !typeUrl.toLowerCase().includes("ed25519"))
        return val;
    return val;
}
// --- Protobuf encoding helpers for CanonicalVote (CometBFT sign bytes) ---
// Wire types: 0=varint, 1=64-bit, 2=length-delimited. Tag = (field << 3) | wire.
function encodeVarint(value) {
    const bytes = [];
    let v = typeof value === "bigint" ? value : BigInt(value);
    const neg = v < BigInt(0);
    if (neg)
        v = (BigInt(1) << BigInt(64)) + v;
    do {
        const low = v & BigInt(0x7f);
        const more = v >> BigInt(7) !== BigInt(0);
        bytes.push(Number(low | (more ? BigInt(0x80) : BigInt(0))));
        v >>= BigInt(7);
    } while (v !== BigInt(0));
    return new Uint8Array(bytes);
}
function encodeTag(field, wire) {
    return new Uint8Array([(field << 3) | wire]);
}
function encodeCanonicalPartSetHeader(total, hash) {
    const tTotal = encodeTag(1, 0);
    const vTotal = encodeVarint(total);
    const tHash = encodeTag(2, 2);
    const vHashLen = encodeVarint(hash.length);
    return concat(tTotal, vTotal, tHash, vHashLen, hash);
}
function encodeCanonicalBlockID(hash, partsTotal, partsHash) {
    const tHash = encodeTag(1, 2);
    const vHashLen = encodeVarint(hash.length);
    const parts = encodeCanonicalPartSetHeader(partsTotal, partsHash);
    const tParts = encodeTag(2, 2);
    const vPartsLen = encodeVarint(parts.length);
    return concat(tHash, vHashLen, hash, tParts, vPartsLen, parts);
}
function encodeTimestamp(seconds, nanos) {
    const tSec = encodeTag(1, 0);
    const vSec = encodeVarint(seconds);
    const tNanos = encodeTag(2, 0);
    const vNanos = encodeVarint(nanos);
    return concat(tSec, vSec, tNanos, vNanos);
}
function concat(...arr) {
    const total = arr.reduce((s, a) => s + a.length, 0);
    const out = new Uint8Array(total);
    let offset = 0;
    for (const a of arr) {
        out.set(a, offset);
        offset += a.length;
    }
    return out;
}
/** Parse ISO 8601 timestamp (e.g. from RPC) to seconds and nanos for CanonicalVote. */
function parseTimestampString(iso) {
    const ms = Date.parse(iso);
    if (Number.isNaN(ms))
        return { seconds: 0, nanos: 0 };
    const seconds = Math.floor(ms / 1000);
    const frac = iso.match(/\.(\d+)Z?$/);
    let nanos = 0;
    if (frac) {
        const fracStr = frac[1].slice(0, 9).padEnd(9, "0");
        nanos = parseInt(fracStr, 10) | 0;
    }
    else {
        nanos = ((ms % 1000) * 1_000_000) | 0;
    }
    return { seconds, nanos };
}
/**
 * Encode CanonicalVote as protobuf (no length prefix here; caller prepends varint for MarshalDelimited).
 * Matches proto/tendermint/types/canonical.proto: type=1, height=2(sfixed64), round=3(sfixed64),
 * block_id=4 (CanonicalBlockID), timestamp=5, chain_id=6. See COMETBFT_SIGNATURE_ENCODING.md.
 * Proto3 omits default-valued fields: round=0 is not encoded (to match CometBFT Go VoteSignBytes).
 */
function encodeCanonicalVoteSignBytes(chainId, blockIdHash, height, round, type, timestampSeconds, timestampNanos, partsTotal, partsHash) {
    const blockId = encodeCanonicalBlockID(blockIdHash, partsTotal, partsHash);
    const timestamp = encodeTimestamp(timestampSeconds, timestampNanos);
    const chainIdBytes = new TextEncoder().encode(chainId);
    const f1 = concat(encodeTag(1, 0), encodeVarint(type));
    const f2 = concat(encodeTag(2, 1), encodeSfixed64(height));
    const f4 = concat(encodeTag(4, 2), encodeVarint(blockId.length), blockId);
    const f5 = concat(encodeTag(5, 2), encodeVarint(timestamp.length), timestamp);
    const f6 = concat(encodeTag(6, 2), encodeVarint(chainIdBytes.length), chainIdBytes);
    // Proto3 omits fields with default value; round=0 is not serialized by Go, so we omit it too.
    if (round !== BigInt(0)) {
        const f3 = concat(encodeTag(3, 1), encodeSfixed64(round));
        return concat(f1, f2, f3, f4, f5, f6);
    }
    return concat(f1, f2, f4, f5, f6);
}
function encodeSfixed64(value) {
    const buf = new Uint8Array(8);
    new DataView(buf.buffer).setBigInt64(0, value, true);
    return buf;
}
/** BlockIDFlag: 1=ABSENT, 2=COMMIT, 3=NIL. Only COMMIT (2) uses the commit's BlockID; others use empty. */
const BLOCK_ID_FLAG_COMMIT = 2;
/**
 * Encode vote sign bytes for CometBFT (same as Go VoteSignBytes):
 * signBytes = MarshalDelimited(CanonicalVote) = varint(len(proto)) || proto.Marshal(CanonicalVote).
 * Uses this CommitSig's timestamp and, per block_id_flag: COMMIT (2) => commit.block_id, else => empty BlockID.
 */
function encodeVoteSignBytes(chainId, commit, sig, type, onDebug) {
    const height = typeof commit.height === "bigint" ? commit.height : BigInt(Number(commit.height));
    const rawCommit = commit;
    const round = typeof rawCommit.round === "number" ? BigInt(rawCommit.round) : BigInt(0);
    // RPC/JSON uses block_id_flag; CosmJS decoded commit uses blockIdFlag (BlockIdFlag.Commit = 2).
    const blockIdFlag = sig.block_id_flag ??
        sig.blockIdFlag;
    // COMMIT (2) => use commit's BlockID; NIL (3) / ABSENT (1) => empty. If missing, assume COMMIT.
    const useCommitBlockId = blockIdFlag === undefined || blockIdFlag === BLOCK_ID_FLAG_COMMIT;
    let blockIdHash;
    let partsTotal;
    let partsHash;
    if (useCommitBlockId && commit.blockId?.hash?.length) {
        blockIdHash = commit.blockId.hash;
        const parts = commit.blockId.parts;
        partsTotal = parts?.total ?? 1;
        partsHash = (parts?.hash?.length === 32 ? parts.hash : blockIdHash);
    }
    else {
        blockIdHash = new Uint8Array(0);
        partsTotal = 0;
        partsHash = new Uint8Array(0);
    }
    let seconds = 0;
    let nanos = 0;
    if (sig.timestamp != null) {
        const t = sig.timestamp;
        if (typeof t.seconds === "number" || typeof t.seconds === "bigint") {
            seconds = t.seconds;
            nanos = Number(t.nanos ?? 0) | 0;
        }
        else if (typeof t.getTime === "function") {
            // Use CosmJS toSeconds so we match exactly how commit timestamps are represented (incl. nanoseconds)
            const s = toSeconds(sig.timestamp);
            seconds = s.seconds;
            nanos = s.nanos | 0;
        }
        else if (typeof sig.timestamp === "string") {
            const parsed = parseTimestampString(sig.timestamp);
            seconds = parsed.seconds;
            nanos = parsed.nanos;
        }
    }
    const protoBody = encodeCanonicalVoteSignBytes(chainId, blockIdHash, height, round, type, seconds, nanos, partsTotal, partsHash);
    const signBytes = concat(encodeVarint(protoBody.length), protoBody);
    if (onDebug) {
        onDebug({
            height,
            round,
            blockIdHash,
            partsTotal,
            partsHash,
            seconds,
            nanos,
            signBytes,
        });
    }
    return signBytes;
}
