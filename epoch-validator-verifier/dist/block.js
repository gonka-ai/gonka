import { getRpcClient } from "./rpc-client.js";
/**
 * Get validators_hash from a block header (block validators, not participants).
 * Supports both validatorsHash (Uint8Array) and validators_hash (base64 string) from RPC.
 */
export function getValidatorsHashFromHeader(header) {
    const vh = header.validatorsHash;
    if (vh != null && vh.length > 0)
        return vh;
    const raw = header.validators_hash;
    if (raw != null && raw !== "")
        return Uint8Array.from(Buffer.from(raw, "base64"));
    return undefined;
}
/**
 * Get block at height. The block includes LastCommit (signatures for the previous block).
 */
export async function getBlock(height) {
    const { client } = await getRpcClient();
    const res = await client.block(height);
    if (!res.block)
        throw new Error(`No block at height ${height}`);
    const block = res.block;
    return {
        block,
        commit: block.lastCommit ?? null,
    };
}
/**
 * Get commit for a specific height (optional; commit is also in block at height+1 as lastCommit).
 */
export async function getCommit(height) {
    const { client } = await getRpcClient();
    const res = await client.commit(height);
    // CosmJS decodeCommitResponse returns { header, commit, canonical }; commit has decoded signatures with timestamp.
    // RPC commit.height is number; we treat as commit type (height normalized to bigint where needed in consumers).
    const commit = res.commit ?? null;
    return commit;
}
/** Page size for validators RPC. Many nodes cap reported total at 100; we paginate by page size to get all. */
const VALIDATORS_PER_PAGE = 100;
/** True if the RPC error means "only page 1 is allowed" (node returns all validators in one page). */
function isSinglePageOnlyError(err) {
    const s = (err instanceof Error ? err.message : "") +
        (typeof err?.data === "string" ? err.data : "") +
        JSON.stringify(err);
    return /page should be within\s*\[\s*1\s*,\s*1\s*\]/i.test(s);
}
/**
 * Get full validator set at a given height (consensus validators who signed that height).
 * Paginates until a page returns fewer than per_page items. If the node only allows page 1
 * (e.g. "page should be within [1, 1]"), we use the validators from the first page.
 */
export async function getValidators(height) {
    const { client } = await getRpcClient();
    const all = [];
    let page = 1;
    for (;;) {
        try {
            const res = await client.validators({
                height,
                page,
                per_page: VALIDATORS_PER_PAGE,
            });
            const list = res.validators ?? [];
            all.push(...[...list]);
            if (list.length < VALIDATORS_PER_PAGE)
                break;
            page++;
        }
        catch (err) {
            if (page > 1 && isSinglePageOnlyError(err))
                break;
            throw err;
        }
    }
    return all;
}
/**
 * Get validator set at a given height together with the block header's validators_hash.
 * Use this when you need a "proof" that the validator set is the one committed in the chain:
 * - The block at height H has header.validators_hash = hash(validator set at H).
 * - The commit (signed by validators at H-1) attests to that header.
 * Full verification: recompute Tendermint's ValidatorSet hash (proto encoding + SHA256) and
 * compare to validatorsHashFromHeader; see docs/validators-with-proof.md.
 */
export async function getValidatorsWithProofContext(height) {
    const [validators, block] = await Promise.all([getValidators(height), getBlock(height)]);
    const header = block.block.header;
    const validatorsHashFromHeader = getValidatorsHashFromHeader(header) ?? header.validatorsHash;
    return {
        validators,
        block,
        validatorsHashFromHeader,
    };
}
function uint8ArrayEqual(a, b) {
    if (a === b)
        return true;
    if (a == null || b == null || a.length !== b.length)
        return false;
    for (let i = 0; i < a.length; i++)
        if (a[i] !== b[i])
            return false;
    return true;
}
/** Canonical fingerprint for validator set comparison (block validators only). */
function validatorSetFingerprint(validators) {
    const parts = [...validators]
        .sort((a, b) => {
        const ah = a.address ? Buffer.from(a.address).toString("hex") : "";
        const bh = b.address ? Buffer.from(b.address).toString("hex") : "";
        return ah.localeCompare(bh);
    })
        .map((v) => {
        const addr = v.address ? Buffer.from(v.address).toString("hex") : "";
        return `${addr}:${v.votingPower.toString()}`;
    });
    return parts.join(",");
}
/**
 * Scan blocks starting at fromHeight and return the first height where the consensus
 * validator set (block validators) changes. Uses block header validators_hash when
 * available; otherwise compares validator lists from /validators.
 * @param fromHeight – start scanning from this block (checks fromHeight+1, +2, …).
 * @param maxBlocks – optional cap (default 10000); stop after this many blocks if no change.
 * @returns The first height > fromHeight where validators differ from the previous block, or null if no change found within the limit.
 */
export async function findHeightWhereValidatorsChange(fromHeight, maxBlocks = 10_000) {
    let prevBlock = await getBlock(fromHeight);
    const prevHeader = prevBlock.block.header;
    let prevHash = getValidatorsHashFromHeader(prevHeader);
    let prevFingerprint = null;
    for (let h = fromHeight + 1; h <= fromHeight + maxBlocks; h++) {
        const block = await getBlock(h);
        const header = block.block.header;
        const currHash = getValidatorsHashFromHeader(header);
        if (prevHash != null && currHash != null && !uint8ArrayEqual(prevHash, currHash)) {
            return h;
        }
        if (prevHash == null || currHash == null) {
            const [prevVals, currVals] = await Promise.all([getValidators(h - 1), getValidators(h)]);
            const prevFp = prevFingerprint ?? validatorSetFingerprint(prevVals);
            const currFp = validatorSetFingerprint(currVals);
            if (prevFp !== currFp)
                return h;
            prevFingerprint = currFp;
        }
        prevBlock = block;
        prevHash = currHash;
    }
    return null;
}
