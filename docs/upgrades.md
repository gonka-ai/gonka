# Upgrade Strategy for Cosmos Blockchain App Using Cosmovisor

## Overview
Our upgrade strategy for the Cosmos blockchain app relies on **Cosmovisor**, a widely used tool in Cosmos environments. Cosmovisor acts as a wrapper around application binaries, monitoring chain events and managing upgrades automatically.

### How Cosmovisor Works:
1. **Event Monitoring**: Cosmovisor listens for chain events and checks the `upgrade-info.json` file in the data directory.
2. **Upgrade Detection**: When the app exits, Cosmovisor reads the `upgrade-info.json` file for details such as:
    - Upgrade name
    - Target chain height
    - URL for the new binaries
3. **Binary Download**: It fetches the specified binaries and relaunches the app using the new binaries with the same arguments.

This approach is central to our strategy for managing application binaries.

---

## Upgrade Types

Our system supports two types of upgrades:

### 1. Full Software Upgrades
Complete chain and API binary upgrades using the standard Cosmos SDK `software-upgrade` command.

### 2. Partial Upgrades
Node version changes and MLNode upgrades using our custom `create-partial-upgrade` command, which allows:
- **Node Version Updates**: Change the version routing without binary upgrades
- **MLNode Upgrades**: Coordinate MLNode version changes with side-by-side deployment strategy

---

## Special Considerations

### Dual Binary Management
Since our system includes two binaries (chain app and decentralized API), we needed to adapt the process for seamless upgrades:

1. **Build Process**:
    - Use the `build-for-upgrade` target in the makefiles for both the chain and API.
    - The process generates a build using Docker, captures the output, and packages it into a zip file.
    - The files are published to the directories `../public-html/v2/dapi` for the decentralized API and `../public-html/v2/inferenced` for the chain binary.
    - The SHA of each build is printed to use in the URL and appended to `../public-html/v2/checksums.txt`.

2. **Governance Proposal**:
    - A governance proposal includes the JSON information about binaries and their SHA.
    - The proposal is submitted and voted on.
    - Once approved, the upgrade details are added to `upgrade-info.json`. Cosmovisor downloads and installs the upgrade.

3. **Scalability Considerations**:
    - The binaries must be hosted in a manner that supports a high volume of simultaneous downloads during the upgrade process.

### MLNode Upgrade Strategy
MLNode upgrades require special handling due to their dependencies and consensus requirements. See `proposals/mlnode-upgrade/README.md` for the complete side-by-side deployment strategy using reverse proxy routing.

---

## Upgrade Workflow

### For Full Software Upgrades:

1. **Chain-Specific Upgrade Handlers**:
    - Add the actual upgrade handler to the chain to manage data migrations or other state updates.
    - Look in the `inference-chain/app/upgrades` folder and the `inference-chain/app/upgrades.go` file for examples
    - Key elements include:
        - Constants defining the upgrade name (in `constants.go`).
        - A `CreateUpgradeHandler` function.
        - Registration in `setupUpgradeHandlers` (found in `upgrades.go`).
        - If the upgrade changes the data in the state, increment `ConsensusVersion` in `module.go`

2. **Build the Binaries**:
    - Generate and publish binaries with the `build-for-upgrade` Makefile target.
    - Ensure appropriate versioning and SHAs.
    - Document the changes clearly.

3. **Submit Governance Proposal**:
    - Include upgrade details such as binaries, SHAs, and hosting URLs.
      Example command line to submit:
   ```bash
   inferenced tx upgrade software-upgrade v0.1.13 \
     --title v0.1.13 \
     --upgrade-height 450 \
     --upgrade-info '{
       "binaries": {
         "linux/amd64": "https://github.com/product-science/race-releases/releases/download/release%2Fv0.1.13/inferenced-amd64.zip?checksum=sha256:42a3b51b89cee9f69df2c4904bcb382af37ed49808b2cb70a512c153fcf8f51d"
       },
       "api_binaries": {
         "linux/amd64": "https://github.com/product-science/race-releases/releases/download/release%2Fv0.1.13/decentralized-api-amd64.zip?checksum=sha256:9514cde587f053b83e8ab804d2e946e20e3b2b426ed0cbf5060cd1ba2df91e15"
       }
     }' \
     --summary "v0.1.13" \
     --deposit 50000000nicoin \
     --from gonka1ysnqfx6s8m47w4r0dcjcvp6l4wkj8v87qm792d \
     --yes --broadcast-mode sync --output json \
     --gas auto --gas-adjustment 1.3
   ```

### For Partial Upgrades (Node Version Only):

1. **Submit Partial Upgrade Proposal**:
   ```bash
   inferenced tx inference create-partial-upgrade [height] [node-version] [api-binaries-json] \
     --from [your-address] \
     --yes --broadcast-mode sync --output json \
     --gas auto --gas-adjustment 1.3
   ```

   Example for node version change only:
   ```bash
   inferenced tx inference create-partial-upgrade 500 "v3.0.8" "" \
     --from gonka1ysnqfx6s8m47w4r0dcjcvp6l4wkj8v87qm792d \
     --yes --broadcast-mode sync --output json \
     --gas auto --gas-adjustment 1.3
   ```

2. **Promote the Proposal**:
    - Allow stakeholders to test the proposed changes.
    - Provide documentation and tools for validating and describing the changes.

3. **Monitor the Rollout**:
    - Upon governance approval, the upgrade will be scheduled for the `upgrade-height`
    - Monitor the network for smooth transition to new version routing.

---

## Upgrading Data in the Chain
The recommended approach for upgrading data is illustrated in PR 84:

https://github.com/product-science/inference-ignite/pull/84

#### Summary:
1. Copy the unmodified .proto files for the data you are changing. Rename them with a V1 in front (or V2 if there is already a V1)
2. Change the original proto files to whatever new format we want
3. Write a custom getter for the OLD values (V1)
4. Register a change handler that gets the old values and then writes the new ones. This may involve:
   1. Converting values from one type to another
   2. Dropping fields
   3. Adding new fields and coming up with a decent default value
   4. Any other necessary change, as long as the new values can be derived from old ones.

---

## Testing the Upgrade Mechanism (General Process)

### Automated Testing:
- Use the `testermint` test suite, particularly `UpgradeTests.kt` and `KubernetesTests.kt`
- Tests include both full software upgrades and partial upgrades
- The `submitUpgradeProposal` helper in `LocalInferencePair.kt` automates proposal submission

### Manual Testing Steps:
1. Update upgrade constants in the appropriate `constants.go` file
    - Set the version to test version (e.g., `v0.0.1-test`).
2. Build and launch the app locally
3. Update the version for the target upgrade (e.g., `v0.0.2-test`) in `constants.go`.
4. Rebuild using `build-for-upgrade` in both `inference-chain` and `decentralized-api`
    - Outputs are stored in the correct directory, and SHAs are output during the build.
5. Run upgrade tests to ensure successful version upgrades.

---

## Testing Specific Upgrades

Testing is similar to above, but with additional steps:

1. **Branch Setup**: You will need TWO branches synced to your local machine:
   - One for the new version you are migrating to
   - One for the commit to upgrade FROM

2. **New Branch Preparation**:
   - Make the changes in the new branch and write the upgrade handler per instructions above
   - Ensure the new binaries will pass tests in the new branch
   - Build the new binaries using `build-for-upgrade` (both in `decentralized-api` and `inference-chain`)

3. **Testing Setup**:
   - Copy the new binaries (zip files) to the OLD branch's binary directory (`../public-html/v2/dapi` and `../public-html/v2/inferenced`)
   - In the OLD branch, update the upgrade test parameters and SHA to match the NEW branch values

4. **Execution**:
   - Launch the chain in the OLD branch
   - Run the upgrade test
   - If successful, run `make build-docker` in the NEW branch to ensure the image matches the new binaries
   - Run verification tests specific to the upgrade (avoid tests that reboot the chain)

---

## Helper Tools and Scripts

### Python Helpers:
- `.github/scripts/execute_voting_update.py` contains automated upgrade proposal generation
- Functions: `generate_upgrade_proposal()` and `get_upgrade_json()`

### Testermint Helpers:
- `testermint/src/main/kotlin/LocalInferencePair.kt` contains `submitUpgradeProposal()` helper
- Supports both full and partial upgrade testing

### Query Commands:
```bash
# List all partial upgrades
inferenced query inference list-partial-upgrade

# Show specific partial upgrade
inferenced query inference show-partial-upgrade [height]
```

---

## Key Notes

- **Two Upgrade Paths**: Use `software-upgrade` for full binary upgrades, `create-partial-upgrade` for node version changes
- **MLNode Strategy**: Requires special side-by-side deployment for consensus-critical upgrades
- **Testing**: Both automated (testermint) and manual testing procedures are available
- **Documentation**: Ensure clear communication of changes and steps for stakeholders
- **Scalability**: Account for possible high traffic during binary downloads
- **Governance**: All upgrades require on-chain governance approval

