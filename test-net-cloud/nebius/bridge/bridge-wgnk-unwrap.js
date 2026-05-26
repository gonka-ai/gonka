const { ethers } = require("ethers");
const fs = require("fs");
const path = require("path");

const args = process.argv.slice(2);
function getArg(flag) {
    const idx = args.indexOf(flag);
    return idx !== -1 ? args[idx + 1] : null;
}

const defaultEthRpcUrl = process.env.SEPOLIA_RPC_URL || "https://ethereum-sepolia-rpc.publicnode.com";
const ethRpcUrl = getArg("--rpc") || defaultEthRpcUrl;
const ethPrivateKey = getArg("--eth-key") || process.env.PRIVATE_KEY;
const bridgeAddress = resolveBridgeAddress();
const amount = getArg("--amount");
const gonkaRecipient = getArg("--gonka-recipient");
const chainName = getArg("--chain") || "ethereum";

if (!ethPrivateKey || !bridgeAddress || !amount) {
    console.log(`
Usage: node bridge-wgnk-unwrap.js \\
  --amount <AMOUNT_NGONKA> \\
  --eth-key <ETH_PRIVATE_KEY> \\
  [--bridge <BRIDGE_CONTRACT_ADDR>] \\
  [--rpc <ETH_RPC_URL>] \\
  [--chain <GONKA_CHAIN_NAME>] \\
  [--gonka-recipient <GONKA_ADDR>]

Example:
  node bridge-wgnk-unwrap.js \\
    --amount 2000 \\
    --eth-key 0xYOUR_ETH_PRIVATE_KEY \\
    --bridge 0xYOUR_BRIDGE_CONTRACT \\
    --gonka-recipient gonka1...

Notes:
  This burns WGNK on Ethereum/Sepolia by transferring it to the bridge contract.
  Gonka release still requires the bridge-exchange validation flow.

Environment:
  BRIDGE_ADDRESS may be used instead of --bridge.
  PRIVATE_KEY may be used instead of --eth-key.
  SEPOLIA_RPC_URL may be used instead of --rpc.
`);
    process.exit(1);
}

function resolveBridgeAddress() {
    const explicit = getArg("--bridge") || process.env.BRIDGE_ADDRESS;
    if (explicit) {
        return explicit;
    }

    const addressFile = path.resolve(__dirname, "../../../proposals/ethereum-bridge-contact/bridge_address.txt");
    if (fs.existsSync(addressFile)) {
        return fs.readFileSync(addressFile, "utf8").trim();
    }

    return "";
}

function maskRpcUrl(url) {
    return url.replace(/\/[a-zA-Z0-9_-]{16,}$/, "/***");
}

async function main() {
    console.log("\n==================================================");
    console.log("Unwrapping WGNK (Ethereum -> Gonka)");
    console.log("==================================================\n");

    const provider = new ethers.JsonRpcProvider(ethRpcUrl);
    const network = await provider.getNetwork();
    console.log(`      > Ethereum RPC: ${maskRpcUrl(ethRpcUrl)} (chain ${network.chainId})`);

    const wallet = new ethers.Wallet(ethPrivateKey, provider);
    const bridge = ethers.getAddress(bridgeAddress);
    const amountBigInt = BigInt(amount);
    if (amountBigInt <= 0n) {
        throw new Error(`Amount must be positive, got ${amount}`);
    }

    const abi = [
        "function name() view returns (string)",
        "function symbol() view returns (string)",
        "function decimals() view returns (uint8)",
        "function balanceOf(address account) view returns (uint256)",
        "function getCurrentState() view returns (uint8)",
        "function transfer(address to, uint256 amount) returns (bool)",
        "event WGNKBurned(address indexed from, uint256 amount, uint256 timestamp)"
    ];
    const contract = new ethers.Contract(bridge, abi, wallet);

    const code = await provider.getCode(bridge);
    if (code === "0x") {
        throw new Error(`No contract found at ${bridge}`);
    }

    const [name, symbol, decimals, state, balanceBefore] = await Promise.all([
        contract.name(),
        contract.symbol(),
        contract.decimals(),
        contract.getCurrentState(),
        contract.balanceOf(wallet.address)
    ]);

    console.log(`      > Bridge/WGNK: ${bridge}`);
    console.log(`      > Wallet:      ${wallet.address}`);
    console.log(`      > Token:       ${name} (${symbol}), decimals=${decimals}`);
    console.log(`      > Amount:      ${amountBigInt} base units (${ethers.formatUnits(amountBigInt, decimals)} ${symbol})`);
    console.log(`      > Balance:     ${balanceBefore} base units`);

    if (state !== 1n) {
        throw new Error(`Bridge contract is not in NORMAL_OPERATION state (state=${state})`);
    }
    if (balanceBefore < amountBigInt) {
        throw new Error(`Insufficient WGNK balance: have ${balanceBefore}, need ${amountBigInt}`);
    }

    console.log("\n[1/1] Burning WGNK by transferring to the bridge contract...");
    const gas = await contract.transfer.estimateGas(bridge, amountBigInt);
    console.log(`      > Estimated Gas: ${gas}`);

    const tx = await contract.transfer(bridge, amountBigInt);
    console.log(`      > Tx Hash: ${tx.hash}`);
    console.log("      > Waiting for confirmation...");
    const receipt = await tx.wait();

    const burnLog = receipt.logs.find(log => {
        try {
            const parsed = contract.interface.parseLog(log);
            return parsed && parsed.name === "WGNKBurned";
        } catch (_) {
            return false;
        }
    });

    if (!burnLog) {
        throw new Error("Transaction confirmed but WGNKBurned event was not found");
    }

    const parsed = contract.interface.parseLog(burnLog);
    console.log(`\nSUCCESS: WGNK burned in block ${receipt.blockNumber}.`);
    console.log(`      > Log Index:   ${burnLog.index}`);
    console.log(`      > From:        ${parsed.args.from}`);
    console.log(`      > Amount:      ${parsed.args.amount}`);
    console.log(`      > Timestamp:   ${parsed.args.timestamp}`);

    if (gonkaRecipient) {
        console.log("\nGonka release helper command:");
        console.log(`./bridge-token-mint-sim.sh --chain ${chainName} \\`);
        console.log(`  --contract ${bridge} \\`);
        console.log(`  --owner ${gonkaRecipient} \\`);
        console.log(`  --amount ${parsed.args.amount} \\`);
        console.log(`  --block ${receipt.blockNumber} \\`);
        console.log(`  --index ${burnLog.index} \\`);
        console.log("  --local");
    } else {
        console.log("\nNext step: submit/validate a Gonka bridge-exchange for this burn.");
        console.log("Pass a Gonka recipient with --gonka-recipient to print the helper command.");
    }
}

main().catch(err => {
    console.error("\nFatal error:");
    console.error(err.message || err);
    process.exit(1);
});
