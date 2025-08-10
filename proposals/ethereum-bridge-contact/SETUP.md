# Bridge Contract Setup Guide

This guide walks you through deploying and configuring the Ethereum bridge contract.

## Prerequisites

1. **Node.js** (v16 or higher)
2. **npm** or **yarn**
3. **Ethereum wallet** with sufficient ETH for deployment
4. **RPC endpoint** for your target network
5. **BLS group public key** for genesis epoch

## Installation

```bash
# Install dependencies
npm install

# Create environment file
cp .env.example .env
```

## Environment Configuration

Create a `.env` file with the following variables:

```bash
# Private key for contract deployment (without 0x prefix)
PRIVATE_KEY=your_private_key_here

# RPC URLs
MAINNET_RPC_URL=https://eth-mainnet.alchemyapi.io/v2/YOUR-API-KEY
SEPOLIA_RPC_URL=https://eth-sepolia.g.alchemy.com/v2/YOUR-API-KEY

# API keys for verification
ETHERSCAN_API_KEY=your_etherscan_api_key

# Bridge configuration
GENESIS_GROUP_PUBLIC_KEY=0x123456...  # 96-byte hex string
ADMIN_ADDRESS=0x742d35Cc6639C0532fBb5Bc...
```

## Deployment Steps

### 1. Compile Contract
```bash
npm run compile
```

### 2. Deploy to Testnet (Sepolia)
```bash
npx hardhat deploy-bridge --network sepolia --verify
```

### 3. Set Up Genesis Epoch
```bash
npx hardhat setup-genesis \
  --bridge 0xYourBridgeAddress \
  --groupkey 0xYour96ByteGroupPublicKey \
  --network sepolia
```

### 4. Verify Contract State
```bash
npx hardhat bridge-status \
  --bridge 0xYourBridgeAddress \
  --network sepolia
```

## Production Deployment

### 1. Security Checklist
- [ ] Use hardware wallet or secure key management
- [ ] Verify RPC endpoint security
- [ ] Test on testnet first
- [ ] Prepare admin multisig wallet
- [ ] Document admin procedures

### 2. Deploy to Mainnet
```bash
npx hardhat deploy-bridge --network mainnet --verify
```

### 3. Post-Deployment
- [ ] Verify contract on Etherscan
- [ ] Set up monitoring and alerts
- [ ] Test with small amounts
- [ ] Document contract addresses
- [ ] Secure admin keys

## Testing

### Unit Tests
```bash
npm test
```

### Gas Analysis
```bash
npm run gas-report
```

### Local Testing
```bash
# Start local node
npm run node

# Deploy locally (in another terminal)
npm run deploy:local
```

## Usage Examples

### Submit New Epoch
```javascript
const bridge = await ethers.getContractAt("BridgeContract", bridgeAddress);
await bridge.submitGroupKey(epochId, groupPublicKey, validationSignature);
```

### Process Withdrawal
```javascript
const withdrawalCommand = {
    epochId: 1,
    requestId: ethers.utils.formatBytes32String("unique-request-id"),
    recipient: "0x...",
    tokenContract: "0x...",
    amount: ethers.utils.parseEther("10"),
    signature: "0x..." // 48-byte BLS signature
};

await bridge.withdraw(withdrawalCommand);
```

### Monitor Events
```javascript
bridge.on("WithdrawalProcessed", (epochId, requestId, recipient, token, amount) => {
    console.log(`Withdrawal: ${amount} ${token} to ${recipient}`);
});

bridge.on("AdminControlActivated", (timestamp, reason) => {
    console.log(`Admin control activated: ${reason}`);
});
```

## Monitoring

### Key Metrics to Monitor
- Contract state (ADMIN_CONTROL vs NORMAL_OPERATION)
- Latest epoch ID and timestamp
- Failed withdrawal attempts
- Admin control activations
- Token balances in contract

### Alerts to Set Up
- Admin control activation
- Timeout warnings (approaching 30 days)
- Large withdrawals
- Failed signature verifications
- Low token balances

## Troubleshooting

### Common Issues

**"InvalidEpochSequence" Error**
- Ensure epoch IDs are submitted sequentially
- Check latest epoch ID: `bridge.getLatestEpochInfo()`

**"InvalidSignature" Error**
- Verify BLS signature format (48 bytes, G1 point)
- Check message encoding: `abi.encodePacked(epochId, requestId, recipient, token, amount)`
- Ensure group public key is correct for the epoch

**"BridgeNotOperational" Error**
- Contract is in ADMIN_CONTROL state
- Check: `bridge.getCurrentState()`
- Admin must call `resetToNormalOperation()`

**Gas Estimation Failures**
- Check token balances in contract
- Verify withdrawal amount doesn't exceed balance
- Ensure recipient address is valid

### Emergency Procedures

**If Contract Stuck in Admin Control:**
1. Check why: `bridge.getCurrentState()`
2. Resolve conflicts (if any)
3. Submit missing epochs
4. Call `resetToNormalOperation()`

**If Funds Stuck:**
1. Funds can only be withdrawn via valid BLS signatures
2. No arbitrary token recovery functions - trustless by design
3. Ensure valid epochs and signatures are available for withdrawals

**If BLS Verification Fails:**
- Verify precompile support on network
- Check BLS library compatibility
- Validate signature generation process

## Security Best Practices

1. **Admin Key Management**
   - Use multisig wallets for admin functions
   - Implement timelock for critical operations
   - Regular key rotation procedures

2. **Monitoring**
   - Set up comprehensive event monitoring
   - Alert on unusual patterns
   - Regular balance reconciliation

3. **Upgrades**
   - Plan for proxy pattern implementation
   - Test upgrade procedures on testnet
   - Coordinate with validator network

4. **Incident Response**
   - Document emergency procedures
   - Test admin control mechanisms
   - Prepare communication channels

## Network-Specific Notes

### Ethereum Mainnet
- Higher gas costs for all operations
- BLS precompile available at address `0x0f`
- Use proper gas estimation

### Arbitrum
- Lower gas costs
- Same BLS precompile address
- Faster block times

### Base
- Similar to Arbitrum
- Good for testing and development
- Lower barriers to entry

### Polygon
- Very low costs
- Check BLS precompile compatibility
- Different gas pricing model