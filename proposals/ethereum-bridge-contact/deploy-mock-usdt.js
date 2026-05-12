// Deployment script for the testnet USDT mock.
// Usage: HARDHAT_NETWORK=sepolia node deploy-mock-usdt.js
//
// Deploys a MockERC20 with name "Tether USD", symbol "USDT", 6 decimals,
// matching the v0.2.13 upgrade handler's expected metadata. The deployer
// becomes the owner and is minted 1,000,000 USDT (1e6 * 1e6 wei) for testing.

import hardhat from "hardhat";
import dotenv from "dotenv";

dotenv.config();

async function getEthers() {
    const connection = await hardhat.network.connect();
    const { ethers } = connection;
    if (!ethers) {
        throw new Error("hardhat-ethers plugin not loaded.");
    }
    return ethers;
}

async function main() {
    const ethers = await getEthers();
    const signers = await ethers.getSigners();
    if (signers.length === 0) {
        throw new Error("No signers available. Set PRIVATE_KEY in .env");
    }
    const deployer = signers[0];
    const deployerAddress = await deployer.getAddress();

    const network = await ethers.provider.getNetwork();
    console.log("Network:", network.name, `(chainId: ${network.chainId})`);
    console.log("Deployer:", deployerAddress);

    const name = "Tether USD";
    const symbol = "USDT";
    const decimals = 6;

    console.log(`\nDeploying MockERC20 (${name}, ${symbol}, ${decimals} decimals)...`);
    const usdt = await ethers.deployContract("MockERC20", [name, symbol, decimals]);
    const deployTx = usdt.deploymentTransaction();
    console.log("Transaction submitted:", deployTx.hash);
    await usdt.waitForDeployment();

    const address = await usdt.getAddress();
    console.log("==================================================");
    console.log("USDT Mock deployed to:", address);
    console.log("==================================================");

    const initialMint = ethers.parseUnits("1000000", decimals);
    console.log("\nMinting 1,000,000 USDT to deployer...");
    const mintTx = await usdt.mint(deployerAddress, initialMint);
    await mintTx.wait();
    console.log("Mint tx:", mintTx.hash);

    const balance = await usdt.balanceOf(deployerAddress);
    console.log("Deployer balance:", ethers.formatUnits(balance, decimals), symbol);

    console.log("\nNext step: copy this address into");
    console.log("  inference-chain/app/upgrades/v0_2_13/upgrades.go");
    console.log(`    USDTContractAddress = "${address}"`);
}

main()
    .then(() => process.exit(0))
    .catch((error) => {
        console.error(error);
        process.exit(1);
    });
