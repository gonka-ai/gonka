# Bridge Testnet Setup Guide (Sepolia)

This guide covers the steps to deploy the bridge contract on Sepolia, register it on Gonka, register the USDC Sepolia implementation, and instantiate the Liquidity Pool.

## 1. Register (Deploy) Bridge Contract on Sepolia

To "register" the bridge contract on Sepolia (the Ethereum testnet), you must deploy the solidity contract.

**Prerequisites:**
- Node.js & npm/yarn
- Sepolia RPC Endpoint (e.g., Alchemy, Infura)
- Private Key with Sepolia ETH (use faucet to get some)
- BLS Group Public Key (from genesis validators)

**Steps:**
1.  **Deploy Bridge to Sepolia**:
    Run the automated setup and deployment script with your private key:
    ```bash
    ./bridge-setup.sh <0xYOUR_PRIVATE_KEY>
    ```
    
    *This script will:*
    *   Fetch the Genesis Group Key from the testnet
    *   Configure your `.env` file
    *   Run the deployment command automatically
    *   **Cleanup**: Removes `.env` (private key) after successful deployment
    
    **Output:** The script will print the **Bridge Contract Address** prominently. Note it down!
    *(Note: Verification is a separate step. You can run `npx hardhat verify --network sepolia <ADDRESS> <ARGS>` if needed)*

---

## 2. Register Bridge on Gonka

Run the registration script directly on the remote node via SSH. This creates a governance proposal for the bridge and USDC metadata.

1.  **Run the Bridge Registration Script**:
    ```bash
    # Usage: ssh user@host "bash -s" -- < test-net-cloud/nebius/bridge/bridge-register.sh --address <BRIDGE_ADDR> [--password <PASS>]
    ssh ubuntu@89.169.110.250 "bash -s" -- < test-net-cloud/nebius/bridge/bridge-register.sh --address 0x190386DAa9205E8Aa494e31d59F9230893Cc60C9
    ```

    *If a proposal was already created but the vote failed or timed out, you can resume with the proposal ID:*
    ```bash
    ssh ubuntu@89.169.110.250 "bash -s" -- < test-net-cloud/nebius/bridge/bridge-register.sh --proposal 1
    ```

    **Verification:** You should see "Vote submitted successfully!" in the output.

---

## 3. Register Wrapped Token Contract

This step registers the code ID of the `wrapped_token.wasm` contract. This code ID is used by the system whenever a new wrapped token is instantiated.

1.  **Run the Wrapped Token Registration Script**:
    You can either upload a new WASM contract or use an existing `code_id`:

    *Option A: Use WASM from Host Repository (Recommended)*
    ```bash
    # Usage: ssh user@host "bash -s" -- < test-net-cloud/nebius/bridge/bridge-register-wrapped.sh --use-repo
    ssh ubuntu@89.169.110.250 "bash -s" -- < test-net-cloud/nebius/bridge/bridge-register-wrapped.sh --use-repo
    ```

    *Option B: Upload Local WASM and Register*
    If you have a local WASM file (e.g. in `proposals/`), upload it first, then register:
    ```bash
    # 1. Upload
    ssh ubuntu@89.169.110.250 "cat > /tmp/wrapped_token.wasm" < inference-chain/contracts/wrapped-token/artifacts/wrapped_token.wasm
    # 2. Register
    ssh ubuntu@89.169.110.250 "bash -s" -- < test-net-cloud/nebius/bridge/bridge-register-wrapped.sh --wasm /tmp/wrapped_token.wasm
    ```

    *Option C: Register using existing Code ID*
    ```bash
    # Usage: ssh user@host "bash -s" -- < test-net-cloud/nebius/bridge/bridge-register-wrapped.sh --code-id <CODE_ID>
    ssh ubuntu@89.169.110.250 "bash -s" -- < test-net-cloud/nebius/bridge/bridge-register-wrapped.sh --code-id 1
    ```

    *If a proposal was already created (Resume/Vote Only):*
    ```bash
    ssh ubuntu@89.169.110.250 "bash -s" -- < test-net-cloud/nebius/bridge/bridge-register-wrapped.sh --proposal 2
    ```

---

## 4. Register Liquidity Pool

This step instantiates the Liquidity Pool contract (WASM) and registers it within the Gonka system.

1.  **Run the Pool Registration Script**:
    You can either upload a new WASM contract or use an existing `code_id`:

    *Option A: Use WASM from Host Repository (Recommended)*
    ```bash
    # Usage: ssh user@host "bash -s" -- < test-net-cloud/nebius/bridge/bridge-register-pool.sh --use-repo
    ssh ubuntu@89.169.110.250 "bash -s" -- < test-net-cloud/nebius/bridge/bridge-register-pool.sh --use-repo
    ```

    *Option B: Upload Local WASM and Register*
    ```bash
    # 1. Upload
    ssh ubuntu@89.169.110.250 "cat > /tmp/liquidity_pool.wasm" < inference-chain/contracts/liquidity-pool/artifacts/liquidity_pool.wasm
    # 2. Register
    ssh ubuntu@89.169.110.250 "bash -s" -- < test-net-cloud/nebius/bridge/bridge-register-pool.sh --wasm /tmp/liquidity_pool.wasm
    ```

    *Option C: Register using existing Code ID*
    ```bash
    # Usage: ssh user@host "bash -s" -- < test-net-cloud/nebius/bridge/bridge-register-pool.sh --code-id <CODE_ID>
    ssh ubuntu@89.169.110.250 "bash -s" -- < test-net-cloud/nebius/bridge/bridge-register-pool.sh --code-id 1
    ```

    *If a proposal was already created (Resume/Vote Only):*
    ```bash
    ssh ubuntu@89.169.110.250 "bash -s" -- < test-net-cloud/nebius/bridge/bridge-register-pool.sh --proposal 4
    ```

    **Verification:** Look for "Vote submitted successfully!" in the output.

---

## 5. Fund Liquidity Pool (Community Pool Spend)

After the Liquidity Pool is registered, it needs to be funded with the 120M GNK from the Community Pool.

1.  **Run the Funding Script**:
    ```bash
    # Usage: ssh user@host "bash -s" -- < test-net-cloud/nebius/bridge/bridge-fund-pool.sh [--amount <AMOUNT>]
    ssh ubuntu@89.169.110.250 "bash -s" -- < test-net-cloud/nebius/bridge/bridge-fund-pool.sh --amount 120000000000000000ngonka
    ```

    *If a proposal was already created (Resume/Vote Only):*
    ```bash
    ssh ubuntu@89.169.110.250 "bash -s" -- < test-net-cloud/nebius/bridge/bridge-fund-pool.sh
    ```

    **Verification:** Look for "Funding proposal submitted and voted successfully!" in the output.

---

## 6. Verify Community Pool Balance

1.  **Check Community Pool Balance**:
    You can verify the funds are available in the community pool:
    ```bash
    ssh ubuntu@89.169.110.250 "/srv/dai/inferenced q distribution community-pool --node http://localhost:8000/chain-rpc/"
    ```
    You should see approximately **120,000,000 GNK** (1.2 * 10^17 ngonka).
