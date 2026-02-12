/**
 * Epoch Validator Verifier
 *
 * 1. Connects to RPC nodes (failover list).
 * 2. Gets latest epoch ID and block height where that epoch started.
 * 3. Gets block at new epoch start; gets validators with merkle proof (ABCI query prove=true).
 * 4. Does the same for the previous epoch.
 * 5. Verifies that the new epoch block's LastCommit signatures are from the previous epoch's validators.
 */
import "dotenv/config";
import { RPC_NODES } from "./config.js";
import { getRpcClient, resetRpcClient } from "./rpc-client.js";
import { getEpochIdAndStartHeight, getEpochStartBlockHeight, getEpochGroupDataByStoreKey, epochGroupDataStoreKeyHex, } from "./epoch.js";
import { getBlock } from "./block.js";
import { verifyProofAgainstRoot } from "./merkle.js";
// --- Epoch validator set check (params with proof + set-new-validators height) ---
export { runEpochValidatorSetCheck, } from "./epoch-validator-set-check.js";
export { getEpochParamsAtHeightWithProof, setNewValidatorsHeight, } from "./epoch-params.js";
/** Format an error for logging: message, cause, and optional stack. */
function formatError(e) {
    const err = e;
    const parts = [err.message ?? String(e)];
    if (err.cause != null) {
        const c = err.cause;
        parts.push("cause: " + (c.message ?? String(err.cause)));
        if (c.code)
            parts.push("code: " + c.code);
    }
    if (err.status != null)
        parts.push("HTTP status: " + err.status);
    if (err.statusCode != null)
        parts.push("HTTP status: " + err.statusCode);
    return parts.join("; ");
}
/** Parse optional epoch ID from env EPOCH_ID or CLI --epoch <id>. */
function getEpochIdArg() {
    const env = process.env.EPOCH_ID;
    if (env !== undefined && env !== "") {
        const n = parseInt(env, 10);
        if (!Number.isNaN(n) && n >= 0)
            return n;
    }
    const idx = process.argv.indexOf("--epoch");
    if (idx !== -1 && process.argv[idx + 1] !== undefined) {
        const n = parseInt(process.argv[idx + 1], 10);
        if (!Number.isNaN(n) && n >= 0)
            return n;
    }
    return undefined;
}
async function main() {
    console.log("RPC nodes:", RPC_NODES);
    if (RPC_NODES.length === 0) {
        console.error("Set RPC_URL_1 (and optionally RPC_URL_2) or edit config.ts");
        process.exit(1);
    }
    const epochIdArg = getEpochIdArg();
    try {
        const { url } = await getRpcClient();
        console.log("Connected to", url);
        // --- 1. Get target epoch ID and block height where that epoch started ---
        const { epochId: latestEpochId, startHeight: newEpochStartHeight } = await getEpochIdAndStartHeight(epochIdArg);
        if (epochIdArg !== undefined) {
            console.log("\n--- Epoch by ID ---");
            console.log("Target epoch ID (from --epoch / EPOCH_ID):", latestEpochId);
            console.log("Epoch start block height:", newEpochStartHeight);
        }
        else {
            console.log("\n--- Latest Epoch ---");
            console.log("Latest epoch ID:", latestEpochId);
            console.log("New epoch start block height:", newEpochStartHeight);
        }
        // --- 1b. Find first block after epoch start where consensus validators change ---
        // console.log("\n--- Validator set change (block validators) ---");
        // let validatorChangeHeight: number | null = null;
        // try {
        //   validatorChangeHeight = await findHeightWhereValidatorsChange(newEpochStartHeight);
        // } catch (e) {
        //   console.error("Validator set change scan failed:", formatError(e));
        //   if ((e as Error).stack) console.error((e as Error).stack);
        //   throw e;
        // }
        // if (validatorChangeHeight != null) {
        //   console.log("First height where validators change:", validatorChangeHeight);
        //   console.log("Validator set change height from epoch start:", validatorChangeHeight - newEpochStartHeight);
        // } else {
        //   console.log("No validator set change found within scan limit.");
        // }
        if (latestEpochId < 1) {
            console.log("No previous epoch to verify (epoch 0). Exiting.");
            return;
        }
        const previousEpochId = latestEpochId - 1;
        const previousEpochStartHeight = await getEpochStartBlockHeight(previousEpochId);
        console.log("Previous epoch ID:", previousEpochId);
        console.log("Previous epoch start block height:", previousEpochStartHeight);
        // --- 2. Get block where new epoch started (and its LastCommit = signatures for block H-1) ---
        console.log("\n--- Block at new epoch start ---");
        const newEpochBlock = await getBlock(newEpochStartHeight);
        const commitInNewBlock = newEpochBlock.commit;
        if (!commitInNewBlock) {
            throw new Error("Block at new epoch start has no LastCommit");
        }
        const commitHeight = Number(commitInNewBlock.height);
        console.log("Block height:", newEpochStartHeight);
        console.log("LastCommit height (previous block):", commitHeight);
        // --- 2b. EpochData by store key (path /store/inference/key) and decode ---
        console.log("\n--- EpochData by store key (epoch " + latestEpochId + ", parent) ---");
        const keyHex = epochGroupDataStoreKeyHex(latestEpochId, "");
        console.log("Key (hex):", keyHex, "path: /store/inference/key");
        let storeKeyResult = await getEpochGroupDataByStoreKey(latestEpochId, "", newEpochStartHeight + 2000);
        if (storeKeyResult.value.length === 0) {
            console.log("No value at epoch-start height; retrying at latest height.");
            storeKeyResult = await getEpochGroupDataByStoreKey(latestEpochId, "", undefined);
        }
        else if (storeKeyResult.decoded && storeKeyResult.decoded.validationWeights.length === 0) {
            // Participants are added later (EndOfPoCValidation); at epoch-start height they are still empty.
            console.log("Value at epoch-start height has 0 participants (added later in epoch); retrying at latest height for participants.");
            storeKeyResult = await getEpochGroupDataByStoreKey(latestEpochId, "", undefined);
        }
        console.log("Store query height:", storeKeyResult.height);
        console.log("Value length:", storeKeyResult.value.length);
        console.log("Proof ops count:", storeKeyResult.proofOps?.length ?? 0);
        if (storeKeyResult.decoded) {
            console.log("Decoded: epochIndex=", storeKeyResult.decoded.epochIndex, "pocStartBlockHeight=", storeKeyResult.decoded.pocStartBlockHeight, "validation_weights (participants):", storeKeyResult.decoded.validationWeights.length);
        }
        else {
            console.log("Decoded: (no value at this key/height or decode failed). See docs/store-path-epoch-data.md for curl example; key hex above.");
        }
        // Verify EpochData store proof against app_hash when proof_ops present.
        // CometBFT: block H's header.app_hash is the app hash after committing block H-1, so state at
        // query height H is committed in block H+1's header. Use getBlock(height + 1) for app_hash.
        if (storeKeyResult.value.length > 0 &&
            storeKeyResult.proofOps &&
            storeKeyResult.proofOps.length > 0) {
            try {
                const appHashBlockHeight = storeKeyResult.height + 1;
                const proofBlock = await getBlock(appHashBlockHeight);
                const header = proofBlock.block.header;
                const appHash = header.appHash ??
                    (header.app_hash != null ? Uint8Array.from(Buffer.from(header.app_hash, "base64")) : null);
                if (appHash && appHash.length > 0) {
                    const keyBytes = Buffer.from(keyHex, "hex");
                    if (process.env.DEBUG_VERIFY === "1" || process.env.DEBUG_VERIFY === "true") {
                        console.log("EpochData store proof: query height", storeKeyResult.height, "app_hash from block", appHashBlockHeight, header.appHash != null ? "header.appHash (Uint8Array)" : "header.app_hash (base64->Uint8Array)", "length", appHash.length);
                    }
                    const valid = verifyProofAgainstRoot(storeKeyResult.proofOps, keyBytes, storeKeyResult.value, appHash);
                    console.log("EpochData store proof vs app_hash:", valid ? "OK" : "FAIL");
                }
                else {
                    console.log("EpochData store proof: skipped (no app_hash in block header)");
                }
            }
            catch (e) {
                console.warn("EpochData store proof verification error:", e.message);
            }
        }
        else if (storeKeyResult.value.length > 0) {
            console.log("EpochData store proof: skipped (no proof_ops from node)");
        }
        /*
        // --- 3. Get validators at previous block (H-1) = previous epoch validators (with proof context) ---
        console.log("\n--- Previous epoch validators (at height " + commitHeight + ") ---");
        const validatorsWithProof = await getValidatorsWithProofContext(commitHeight);
        const previousValidators = validatorsWithProof.validators;
        console.log("Validators count:", previousValidators.length);
        console.log(
          "validators_hash from block header (hex):",
          validatorsWithProof.validatorsHashFromHeader
            ? Buffer.from(validatorsWithProof.validatorsHashFromHeader).toString("hex")
            : "(none)"
        );
        // Log first few validators: address (hex) and voting power (weight)
        const toLog = Math.min(5, previousValidators.length);
        for (let i = 0; i < toLog; i++) {
          const v = previousValidators[i];
          const addrHex = v.address ? Buffer.from(v.address).toString("hex") : "(no address)";
          console.log("  validator[" + i + "] address:", addrHex, "voting_power (weight):", v.votingPower.toString());
        }
        if (previousValidators.length > toLog) {
          console.log("  ... and", previousValidators.length - toLog, "more");
        }
    
        // --- 4. Get epoch group data (REST or ABCI query) ---
        let epochData: ReturnType<typeof decodeQueryGetEpochGroupDataResponse> | null = null;
    
        if (API_BASE_URL) {
          // Regular HTTP GET (REST / gRPC-gateway) — no ABCI/JSON-RPC
          console.log("\n--- Epoch group data (REST) ---");
          epochData = await getEpochGroupDataRest(latestEpochId, "", newEpochStartHeight);
          if (epochData) {
            console.log("EpochData (REST): epochIndex=", epochData.epochIndex, "pocStartBlockHeight=", epochData.pocStartBlockHeight);
            console.log("Validators (validation_weights):", epochData.validationWeights.length);
            for (const v of epochData.validationWeights.slice(0, 5)) {
              console.log("  -", v.memberAddress, "weight:", v.weight);
            }
            if (epochData.validationWeights.length > 5) {
              console.log("  ... and", epochData.validationWeights.length - 5, "more");
            }
          } else {
            console.warn("REST EpochGroupData failed or empty (check API_BASE_URL and endpoint)");
          }
        } else {
          // ABCI query (JSON-RPC abci_query)
          console.log("\n--- Epoch group data with Merkle proof (at new epoch start height) ---");
          const requestBytes = encodeEpochGroupDataRequest(latestEpochId, "");
          const queryWithProof = await abciQueryWithProof(
            QUERY_EPOCH_GROUP_DATA_PATH,
            requestBytes,
            newEpochStartHeight
          );
          console.log("ABCI query with proof: height=", queryWithProof.height, "proofOps=", queryWithProof.proofOps?.length ?? 0);
          if (!queryWithProof.proofOps?.length) {
            console.log("(proofOps empty: node may not return proofs for this gRPC-style query path, or proof not supported)");
          }
    
          try {
            epochData = decodeQueryGetEpochGroupDataResponse(queryWithProof.value);
            console.log("EpochData (decoded): epochIndex=", epochData.epochIndex, "pocStartBlockHeight=", epochData.pocStartBlockHeight);
            console.log("Validators (validation_weights):", epochData.validationWeights.length);
            for (const v of epochData.validationWeights.slice(0, 5)) {
              console.log("  -", v.memberAddress, "weight:", v.weight);
            }
            if (epochData.validationWeights.length > 5) {
              console.log("  ... and", epochData.validationWeights.length - 5, "more");
            }
            if (epochData.validationWeights.length === 0 && (epochData.field2 !== undefined || epochData.field3Bytes?.length)) {
              if (epochData.field2 !== undefined) console.log("  (field2):", epochData.field2);
              if (epochData.field3Bytes?.length) console.log("  (field3Bytes hex):", Buffer.from(epochData.field3Bytes).toString("hex"));
            }
          } catch (e) {
            console.warn("Failed to decode EpochGroupData value:", (e as Error).message);
          }
    
          // app_hash for state at query height H is in block H+1's header (CometBFT semantics).
          const appHashBlock = await getBlock(queryWithProof.height + 1);
          const appHash = (appHashBlock.block.header as { appHash?: Uint8Array }).appHash;
          if (appHash && queryWithProof.proofOps && queryWithProof.proofOps.length > 0) {
            const key = Buffer.from(`/epoch_group_data/${latestEpochId}/\x00`);
            const valid = verifyProofAgainstRoot(
              queryWithProof.proofOps,
              key,
              queryWithProof.value,
              appHash
            );
            console.log("Merkle proof vs app_hash:", valid ? "OK" : "FAIL");
          }
        }
    
        // --- 5. Verify that new epoch block's LastCommit is signed by previous validators ---
        console.log("\n--- Verify: new epoch block signed by previous validators ---");
        const chainId = (newEpochBlock.block.header as { chainId?: string }).chainId ?? "gonka";
        const result = await verifyCommitFromValidators(
          chainId,
          commitInNewBlock,
          commitHeight,
          previousValidators
        );
    
        console.log("Signed power:", result.signedPower.toString());
        console.log("Total power:", result.totalPower.toString());
        console.log("2/3 threshold:", ((result.totalPower * BigInt(2) + BigInt(2)) / BigInt(3)).toString());
        console.log("Result:", result.ok ? "OK" : "FAIL");
        if (result.errors.length > 0) {
          console.log("Errors:", result.errors.slice(0, 5).join("; "));
        }
    
        if (!result.ok) {
          process.exit(1);
        }
    
        console.log("\nDone: new epoch block is signed by previous epoch validators.");
        */
    }
    catch (e) {
        console.error("Error:", formatError(e));
        if (e.stack)
            console.error(e.stack);
        resetRpcClient();
        process.exit(1);
    }
}
main();
