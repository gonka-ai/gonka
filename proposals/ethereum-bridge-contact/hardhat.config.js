import hardhatEthers from "@nomicfoundation/hardhat-ethers";
import hardhatLedger from "@nomicfoundation/hardhat-ledger";
import dotenv from "dotenv";

dotenv.config();

// Ledger address (set via LEDGER_ADDRESS env var or hardcode here)
const LEDGER_ADDRESS = process.env.LEDGER_ADDRESS || "0xc0eF863B1c566F82da3cD2Dd083c6716963452bF";

/** @type import('hardhat/config').HardhatUserConfig */
export default {
  plugins: [hardhatEthers, hardhatLedger],
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
    // Local development network (use --fork for mainnet forking)
    hardhat: {
      type: "edr-simulated",
      chainId: 31337,
    },
    
    // Localhost (for running against 'npx hardhat node')
    localhost: {
      type: "http",
      url: "http://127.0.0.1:8545",
      chainId: 31337,
    },
    
    // Ethereum mainnet
    mainnet: {
      type: "http",
      url: process.env.MAINNET_RPC_URL || "https://eth-mainnet.alchemyapi.io/v2/YOUR-API-KEY",
      accounts: process.env.PRIVATE_KEY ? [process.env.PRIVATE_KEY] : [],
      ledgerAccounts: [LEDGER_ADDRESS],
      chainId: 1,
    },
    
    // Ethereum Sepolia testnet
    sepolia: {
      type: "http",
      url: process.env.SEPOLIA_RPC_URL || "https://eth-sepolia.g.alchemy.com/v2/YOUR-API-KEY",
      accounts: process.env.PRIVATE_KEY ? [process.env.PRIVATE_KEY] : [],
      chainId: 11155111,
    },
    
    // Fusaka testnet (has BLS precompile support via EIP-2537)
    fusaka: {
      type: "http",
      url: process.env.FUSAKA_RPC_URL || "https://rpc.fusaka.ethpandaops.io",
      accounts: process.env.PRIVATE_KEY ? [process.env.PRIVATE_KEY] : [],
      chainId: 1711,  // Fusaka chain ID
    },
    
    // Arbitrum One
    arbitrum: {
      type: "http",
      url: process.env.ARBITRUM_RPC_URL || "https://arb1.arbitrum.io/rpc",
      accounts: process.env.PRIVATE_KEY ? [process.env.PRIVATE_KEY] : [],
      chainId: 42161,
    },
    
    // Polygon
    polygon: {
      type: "http",
      url: process.env.POLYGON_RPC_URL || "https://polygon-rpc.com",
      accounts: process.env.PRIVATE_KEY ? [process.env.PRIVATE_KEY] : [],
      chainId: 137,
    },
    
    // Base
    base: {
      type: "http",
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