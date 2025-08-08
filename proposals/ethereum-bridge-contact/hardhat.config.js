require("@nomicfoundation/hardhat-toolbox");
require("dotenv").config();

/** @type import('hardhat/config').HardhatUserConfig */
module.exports = {
  solidity: {
    version: "0.8.19",
    settings: {
      optimizer: {
        enabled: true,
        runs: 200,
      },
    },
  },
  networks: {
    // Local development network
    hardhat: {
      chainId: 31337,
    },
    
    // Ethereum mainnet
    mainnet: {
      url: process.env.MAINNET_RPC_URL || "https://eth-mainnet.alchemyapi.io/v2/YOUR-API-KEY",
      accounts: process.env.PRIVATE_KEY ? [process.env.PRIVATE_KEY] : [],
      chainId: 1,
    },
    
    // Ethereum Sepolia testnet
    sepolia: {
      url: process.env.SEPOLIA_RPC_URL || "https://eth-sepolia.g.alchemy.com/v2/YOUR-API-KEY",
      accounts: process.env.PRIVATE_KEY ? [process.env.PRIVATE_KEY] : [],
      chainId: 11155111,
    },
    
    // Arbitrum One
    arbitrum: {
      url: process.env.ARBITRUM_RPC_URL || "https://arb1.arbitrum.io/rpc",
      accounts: process.env.PRIVATE_KEY ? [process.env.PRIVATE_KEY] : [],
      chainId: 42161,
    },
    
    // Polygon
    polygon: {
      url: process.env.POLYGON_RPC_URL || "https://polygon-rpc.com",
      accounts: process.env.PRIVATE_KEY ? [process.env.PRIVATE_KEY] : [],
      chainId: 137,
    },
    
    // Base
    base: {
      url: process.env.BASE_RPC_URL || "https://mainnet.base.org",
      accounts: process.env.PRIVATE_KEY ? [process.env.PRIVATE_KEY] : [],
      chainId: 8453,
    },
  },
  
  etherscan: {
    apiKey: {
      mainnet: process.env.ETHERSCAN_API_KEY,
      sepolia: process.env.ETHERSCAN_API_KEY,
      arbitrumOne: process.env.ARBISCAN_API_KEY,
      polygon: process.env.POLYGONSCAN_API_KEY,
      base: process.env.BASESCAN_API_KEY,
    },
  },
  
  gasReporter: {
    enabled: process.env.REPORT_GAS !== undefined,
    currency: "USD",
  },
  
  // Contract verification settings
  sourcify: {
    enabled: true,
  },
};

// Tasks for common operations
task("deploy-bridge", "Deploy the BridgeContract")
  .addOptionalParam("verify", "Verify contract on Etherscan", false, types.boolean)
  .setAction(async (taskArgs, hre) => {
    const { main } = require("./deploy.js");
    const bridge = await main();
    
    if (taskArgs.verify && hre.network.name !== "hardhat") {
      console.log("Waiting for block confirmations...");
      await bridge.deployTransaction.wait(6);
      
      console.log("Verifying contract...");
      await hre.run("verify:verify", {
        address: bridge.address,
        constructorArguments: [],
      });
    }
    
    return bridge;
  });

task("setup-genesis", "Setup genesis epoch for deployed bridge")
  .addParam("bridge", "Bridge contract address")
  .addParam("groupkey", "96-byte hex group public key")
  .setAction(async (taskArgs, hre) => {
    const { submitGenesisEpoch, enableNormalOperation } = require("./deploy.js");
    
    console.log("Setting up genesis epoch...");
    await submitGenesisEpoch(taskArgs.bridge, taskArgs.groupkey);
    
    console.log("Enabling normal operation...");
    await enableNormalOperation(taskArgs.bridge);
    
    console.log("Bridge setup complete!");
  });

task("bridge-status", "Check bridge contract status")
  .addParam("bridge", "Bridge contract address")
  .setAction(async (taskArgs, hre) => {
    const BridgeContract = await hre.ethers.getContractFactory("BridgeContract");
    const bridge = BridgeContract.attach(taskArgs.bridge);
    
    const state = await bridge.getCurrentState();
    const epochInfo = await bridge.getLatestEpochInfo();
    const isTimeout = await bridge.isTimeoutReached();
    
    console.log("Bridge Status:");
    console.log("- Address:", bridge.address);
    console.log("- State:", state === 0 ? "ADMIN_CONTROL" : "NORMAL_OPERATION");
    console.log("- Latest Epoch:", epochInfo.epochId.toString());
    console.log("- Last Update:", new Date(epochInfo.timestamp.toNumber() * 1000).toISOString());
    console.log("- Timeout Reached:", isTimeout);
    console.log("- Owner:", await bridge.owner());
  });

task("test-withdrawal", "Test a withdrawal (placeholder signature)")
  .addParam("bridge", "Bridge contract address")
  .addParam("epoch", "Epoch ID")
  .addParam("request", "Request ID (string)")
  .addParam("recipient", "Recipient address")
  .addParam("token", "Token contract address")
  .addParam("amount", "Amount to withdraw (in wei)")
  .setAction(async (taskArgs, hre) => {
    const { testWithdrawal, createWithdrawalCommand } = require("./deploy.js");
    
    const withdrawalCommand = createWithdrawalCommand(
      parseInt(taskArgs.epoch),
      hre.ethers.utils.formatBytes32String(taskArgs.request),
      taskArgs.recipient,
      taskArgs.token,
      hre.ethers.BigNumber.from(taskArgs.amount)
    );
    
    console.log("Testing withdrawal with placeholder signature...");
    console.log("Note: This will fail signature verification unless using actual BLS signature");
    
    try {
      await testWithdrawal(taskArgs.bridge, withdrawalCommand);
    } catch (error) {
      console.log("Expected failure due to placeholder signature:", error.message);
    }
  });