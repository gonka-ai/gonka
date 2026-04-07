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
    # Usage: ssh user@host "bash -s" -- < test-net-cloud/nebius/bridge/bridge-pool-register.sh --use-repo
    ssh ubuntu@89.169.110.250 "bash -s" -- < test-net-cloud/nebius/bridge/bridge-pool-register.sh --use-repo
    ```

    *Option B: Upload Local WASM and Register*
    ```bash
    # 1. Upload
    ssh ubuntu@89.169.110.250 "cat > /tmp/liquidity_pool.wasm" < inference-chain/contracts/liquidity-pool/artifacts/liquidity_pool.wasm
    # 2. Register
    ssh ubuntu@89.169.110.250 "bash -s" -- < test-net-cloud/nebius/bridge/bridge-pool-register.sh --wasm /tmp/liquidity_pool.wasm
    ```

    *Option C: Register using existing Code ID*
    ```bash
    # Usage: ssh user@host "bash -s" -- < test-net-cloud/nebius/bridge/bridge-pool-register.sh --code-id <CODE_ID>
    ssh ubuntu@89.169.110.250 "bash -s" -- < test-net-cloud/nebius/bridge/bridge-pool-register.sh --code-id 1
    ```

    *If a proposal was already created (Resume/Vote Only):*
    ```bash
    ssh ubuntu@89.169.110.250 "bash -s" -- < test-net-cloud/nebius/bridge/bridge-pool-register.sh --proposal 4
    ```

    **Verification:** Look for "Vote submitted successfully!" in the output.

---

## 5. Fund Liquidity Pool (Community Pool Spend)

After the Liquidity Pool is registered, it needs to be funded with the 120M GNK from the Community Pool.

1.  **Run the Funding Script**:
    ```bash
    # Usage: ssh user@host "bash -s" -- < test-net-cloud/nebius/bridge/bridge-pool-fund.sh [--amount <AMOUNT>]
    ssh ubuntu@89.169.110.250 "bash -s" -- < test-net-cloud/nebius/bridge/bridge-pool-fund.sh --amount 120000000000000000ngonka
    ```

    *If a proposal was already created (Resume/Vote Only):*
    ```bash
    ssh ubuntu@89.169.110.250 "bash -s" -- < test-net-cloud/nebius/bridge/bridge-pool-fund.sh
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

---

## 7. Update (Migrate) Liquidity Pool Contract

If you need to update the Liquidity Pool contract logic (e.g. bug fixes), you can migrate it to a new Code ID via governance.

1.  **Run Migration Script**:
    You can either upload a new WASM file (which registers a new Code ID automatically) or use an existing Code ID.

    **Option A: Upload new WASM and Migrate (Recommended)**
    ```bash
    # Usage: ssh user@host "bash -s" -- < test-net-cloud/nebius/bridge/bridge-pool-migrate.sh --wasm <LOCAL_PATH>
    # Example (uploading local build):
    ssh ubuntu@89.169.110.250 "cat > /tmp/liquidity_pool.wasm" < inference-chain/contracts/liquidity-pool/artifacts/liquidity_pool.wasm
    ssh ubuntu@89.169.110.250 "bash -s" -- < test-net-cloud/nebius/bridge/bridge-pool-migrate.sh --wasm /tmp/liquidity_pool.wasm
    ```

    **Option B: Use Existing Code ID**
    ```bash
    # Usage: ssh user@host "bash -s" -- < test-net-cloud/nebius/bridge/bridge-pool-migrate.sh --code-id <NEW_CODE_ID>
    ssh ubuntu@89.169.110.250 "bash -s" -- < test-net-cloud/nebius/bridge/bridge-pool-migrate.sh --code-id 5
    ```
    *This creates a proposal to migrate the contract to the new Code ID. Once passed, the contract is updated in place.*

---

## 8. Setup IBC Relayer (Hermes)

To transfer tokens between Gonka Testnet and other chains (e.g., Injective Testnet), you need an IBC Relayer.

**Prerequisites on the Server:**
The setup scripts require the following tools: `sudo`, `curl`, `tar`, `jq`, and a configured `inferenced` binary.
*(These are generally available on the standard Ubuntu testnet image)*

1.  **Install & Configure Hermes**:
    Run this script to install Hermes, generate `config.toml`, and auto-generate requisite keys.
    ```bash
    # Usage: ssh user@host "bash -s" -- < test-net-cloud/nebius/bridge/bridge-ibc-setup-hermes.sh
    ssh ubuntu@89.169.110.250 "bash -s" -- < test-net-cloud/nebius/bridge/bridge-ibc-setup-hermes.sh
    ```
    *   **Auto-Funding**: The script attempts to fund the gonka-relayer address from your main account.
    *   **Injective Funding**: You **MUST** manually fund the generated Injective address (`inj1...`) using the [Injective Testnet Faucet](https://testnet.faucet.injective.network/). **Important:** You must fund this address with both INJ (for gas) *and* the specific token you want to bridge (e.g., USDT), as the transfer will originate from this relayer account.

2.  **Create IBC Connection & Channel**:
    Establish a connection and channel between Gonka and Injective:
    ```bash
    ssh ubuntu@89.169.110.250 "bash -s" -- < test-net-cloud/nebius/bridge/bridge-ibc-create-channel.sh
    ```
    *This script verifies/creates the Connection first, then creates the Channel.*

    **IMPORTANT:** The script output will display the **Gonka Channel ID** (e.g., `channel-0`) and **Injective Channel ID** (e.g., `channel-1`). **Record these IDs!** You will need them for initiating transfers.

3.  **Start Relayer**:
    Start the relayer service:
    ```bash
    ssh ubuntu@89.169.110.250 "hermes start"
    ```
    *(Ideally run this in a screen session or systemd service)*

---

## 9. Transfer Tokens from Injective (Fund Account)

Before you can register/approve an IBC token (e.g. Injective USDT), you must transfer some of it to Gonka to generate the `ibc/HASH`.

1.  **Transfer from Injective (via Script)**:
    Use the `bridge-transfer-from-injective.sh` script to send tokens.
    *   **--channel**: The Injective-side Channel ID (from Step 8.2).
    *   **--amount**: Amount in base units. For USDT (6 decimals), 1 USDT = `1000000`.
    *   **--denom**: The denom on Injective. For Testnet USDT, usually a `peggy0x...` address. 
        *(Note: Verify the exact current denom on the Injective Testnet Explorer or your Keplr wallet, as testnet token addresses may change).*
    *   **Default Receiver**: proper account `gonka-account-key` (automatically fetched).

    ```bash
    # Usage: ssh user@host "bash -s" -- < test-net-cloud/nebius/bridge/bridge-ibc-transfer-inj.sh --channel <INJ_CHANNEL_ID> [--amount <AMT>] [--denom <DENOM>]
    
    # Example (Sending 1 USDT):
    ssh ubuntu@89.169.110.250 "bash -s" -- < test-net-cloud/nebius/bridge/bridge-ibc-transfer-inj.sh --channel channel-1 --amount 1000000 --denom peggy0x87aB3B4C8691eD1a84a9191d90237C6C275EDa59
    ```

2.  **Alternative: Manual Transfer via Keplr Wallet**:
    If you prefer to use a wallet UI instead of the script:
    1. Open **Keplr** and ensure both Injective Testnet and Gonka Testnet are added.
    2. Go to Keplr Settings > Advanced > Enable **"Developer Mode"** (to enable Advanced IBC Transfers).
    3. On the Injective Testnet asset list, select the token (e.g., USDT) and choose to **Send** / **IBC Send**.
    4. Click **New IBC Transfer Channel**.
    5. Select Destination Chain: `Gonka Testnet`.
    6. Enter the **Source Channel ID**: `<INJ_CHANNEL_ID>` (the Injective Chain Channel ID you recorded in Step 8.2).
    7. Save the channel, enter your Gonka testnet address (`gonka1...`) as the recipient, specify the amount, and officially approve the transaction.

3.  **Find the IBC Denom (ibc/HASH)**:
    After the transfer (and relayer processes it), check your Gonka balance (`gonka-account-key`):
    ```bash
    ssh ubuntu@89.169.110.250 "/srv/dai/inferenced q bank balances \$(/srv/dai/inferenced keys show gonka-account-key -a --keyring-backend file --keyring-dir /srv/dai/.inference) --node http://localhost:8000/chain-rpc/ --output json"
    ```
    Look for a denom starting with `ibc/...` (e.g. `ibc/4CD24...`).
    **Copy this hash.** You will need it for registration.

    > **Troubleshooting: Transfer Stuck?**
    > If funds don't arrive, check for pending packets on the source chain (Injective):
    > ```bash
    > ssh ubuntu@89.169.110.250 "hermes query packet pending --chain injective-888 --port transfer --channel <INJ_CHANNEL_ID>"
    > ```
    > If packets are stuck, try clearing them:
    > ```bash
    > ssh ubuntu@89.169.110.250 "hermes clear packets --chain injective-888 --port transfer --channel <INJ_CHANNEL_ID>"
    > ```

---

## 10. Register & Approve IBC Token

To enable trading for the IBC token you just transferred, you must register its metadata and approve it for trading via governance.

1.  **Run the Approval Script**:
    ```bash
    # Usage: ssh user@host "bash -s" -- < test-net-cloud/nebius/bridge/bridge-ibc-approve-token.sh --ibc-denom <DENOM> --symbol <SYM> --name <NAME>
    
    # Example for Injective USDT:
    ssh ubuntu@89.169.110.250 "bash -s" -- < test-net-cloud/nebius/bridge/bridge-ibc-approve-token.sh \
      --ibc-denom ibc/4CD24... \
      --symbol USDT \
      --name "Injective USDT" \
      --decimals 6 \
      --counterparty-chain "injective-888"
    ```

    *If a proposal was already created (Resume/Vote Only):*
    ```bash
    ssh ubuntu@89.169.110.250 "bash -s" -- < test-net-cloud/nebius/bridge/bridge-ibc-approve-token.sh --proposal <PROPOSAL_ID>
    ```

    **Verification:** 
    1. Look for "Vote submitted successfully!" in the output and note the **Proposal ID**.
    2. Wait 1-2 minutes for the voting period to end.
    3. Verify the proposal shows **PROPOSAL_STATUS_PASSED**:
       ```bash
       ssh ubuntu@89.169.110.250 "/srv/dai/inferenced q gov proposal <PROPOSAL_ID> --node http://localhost:8000/chain-rpc/ --output json | jq .status"
       ```
    4. Verify the Liquidity Pool recognizes the token for trading:
       ```bash
       ssh ubuntu@89.169.110.250 "LP_ADDR=\$(/srv/dai/inferenced q inference registered-liquidity-pool-address -o json --node http://localhost:8000/chain-rpc/ | jq -r '.address') && /srv/dai/inferenced q wasm contract-state smart \$LP_ADDR '{\"validate_ibc_token_for_trade\": {\"denom\": \"ibc/4CD24...\"}}' --node http://localhost:8000/chain-rpc/ -o json"
       ```
       *(This should return the expected decimals and confirm validity).*

---

## 11. Test Trade Execution

Once the Liquidity Pool is funded and an IBC token is approved, you can verify the trading functionality by executing a test trade.

1.  **Run IBC Token Trade Test (Swap IBC -> GNK)**:
    This tests the standard flow: purchasing GNK using an IBC token (e.g., bridged USDT).
    ```bash
    # Usage: ssh user@host "bash -s" -- < test-net-cloud/nebius/bridge/bridge-pool-test-trade.sh --ibc-denom <DENOM> [--amount <AMOUNT>]
    # Example (Swapping 1 USDT):
    ssh ubuntu@89.169.110.250 "bash -s" -- < test-net-cloud/nebius/bridge/bridge-pool-test-trade.sh --ibc-denom ibc/4CD24... --amount 1000000
    ```
    *The script will automatically fetch the registered Liquidity Pool address.*

    **Verification:** Look for **"SUCCESS! Trade executed successfully."** in the output.
    You can also check the transaction hash on the explorer or query the tx details to see token transfer events.

2.  **Run Wrapped Token Trade Test (Swap Wrapped -> GNK)**:
    This tests the bridge flow: purchasing GNK using a wrapped bridge token (e.g., bridged ETH/USDC).
    ```bash
    # Usage: ssh user@host "bash -s" -- < test-net-cloud/nebius/bridge/bridge-pool-test-trade.sh --cw20 <ADDR> [--amount <AMOUNT>]
    ssh ubuntu@89.169.110.250 "bash -s" -- < test-net-cloud/nebius/bridge/bridge-pool-test-trade.sh --cw20 <CW20_ADDR> --amount 1000000
    ```
    *This executes a CW20 "Send" to the Liquidity Pool with the purchase hook payload.*

    **Verification:** Look for **"SUCCESS! Wrapped Token Trade executed successfully."** in the output.
