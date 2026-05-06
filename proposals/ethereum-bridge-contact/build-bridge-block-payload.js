#!/usr/bin/env node
/**
 * Build a bridge block payload for manual POST to the decentralized API.
 * Use when geth/bridge is not posting (e.g. sync stuck). Requires the same
 * PRIVATE_KEY that sent the USDC transfer (to derive publicKey for the receipt).
 *
 * Usage:
 *   TX_HASH=0x... node build-bridge-block-payload.js     # output JSON to stdout
 *   TX_HASH=0x... node build-bridge-block-payload.js > payload.json
 *   node build-bridge-block-payload.js 0x...             # tx hash as first arg
 *
 * Then POST from host or bridge container:
 *   curl -X POST http://127.0.0.1:9200/admin/v1/bridge/block \
 *     -H "Content-Type: application/json" -d @payload.json
 */

import { ethers } from "ethers";
import dotenv from "dotenv";
import { secp256k1 } from "@noble/curves/secp256k1";

dotenv.config();

const TRANSFER_TOPIC = ethers.id("Transfer(address,address,uint256)");
const USDC_SEPOLIA = "0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238";

function getUncompressedPublicKeyHex(privateKeyHex) {
  const bytes = ethers.getBytes(privateKeyHex);
  const pub = secp256k1.getPublicKey(bytes, false);
  return ethers.hexlify(pub);
}

async function main() {
  const txHash = process.env.TX_HASH || process.argv[2];
  if (!txHash || !ethers.isHexString(txHash)) {
    console.error("Usage: TX_HASH=0x... node build-bridge-block-payload.js");
    console.error("   or: node build-bridge-block-payload.js <txHash>");
    process.exit(1);
  }

  const rpcUrl = process.env.SEPOLIA_RPC_URL;
  const privateKey = process.env.PRIVATE_KEY;
  if (!rpcUrl || !privateKey) {
    console.error("Set SEPOLIA_RPC_URL and PRIVATE_KEY in .env");
    process.exit(1);
  }

  const provider = new ethers.JsonRpcProvider(rpcUrl);
  const wallet = new ethers.Wallet(privateKey, provider);

  const tx = await provider.getTransaction(txHash);
  const receipt = await provider.getTransactionReceipt(txHash);
  if (!tx || !receipt) {
    console.error("Tx or receipt not found for", txHash);
    process.exit(1);
  }
  const block = await provider.getBlock(receipt.blockNumber);

  if (!block) {
    console.error("Block not found for blockNumber", receipt.blockNumber);
    process.exit(1);
  }

  if (ethers.getAddress(tx.from) !== ethers.getAddress(wallet.address)) {
    console.error("PRIVATE_KEY does not match tx sender. Tx from:", tx.from, "Wallet:", wallet.address);
    process.exit(1);
  }

  const transferLog = receipt.logs.find(
    (l) => l.topics[0] === TRANSFER_TOPIC && ethers.getAddress(l.address) === ethers.getAddress(USDC_SEPOLIA)
  );
  if (!transferLog) {
    console.error("No USDC Transfer log in this tx. Is this the USDC transfer to the bridge?");
    process.exit(1);
  }

  const amount = transferLog.data === "0x" ? "0" : ethers.toBigInt(transferLog.data).toString();
  const contractAddress = ethers.getAddress(transferLog.address);
  const ownerAddress = ethers.getAddress(tx.from);
  const receiptIndex = String(receipt.index);
  const publicKey = getUncompressedPublicKeyHex(wallet.signingKey.privateKey);
  const publicKeyHex = publicKey.startsWith("0x") ? publicKey.slice(2) : publicKey;

  const payload = {
    blockNumber: String(block.number),
    originChain: "sepolia",
    receiptsRoot: block.receiptsRoot,
    receipts: [
      {
        contract: contractAddress,
        owner: ownerAddress,
        publicKey: publicKeyHex,
        amount,
        receiptIndex,
      },
    ],
  };

  console.log(JSON.stringify(payload, null, 2));
}

main().catch((e) => {
  console.error(e);
  process.exit(1);
});
