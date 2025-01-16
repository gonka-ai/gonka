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

## Special Considerations

### Dual Binary Management
Since our system includes two binaries (chain app and decentralized API), we needed to adapt the process for seamless upgrades:

1. **Build Process**:
    - Use the `build-for-upgrade` target in the makefiles for both the chain and API.
    - The process generates a build using Docker, captures the output, and packages it into a zip file.
    - The files are published to the directories `public_html/v2/dapi` for the decentralized API and `public_html/v2/inferenced` for the chain binary.
    - The SHA of each build is printed to use in the URL.

2. **Governance Proposal**:
    - A governance proposal includes the JSON information about binaries and their SHA.
    - The proposal is submitted and voted on.
    - Once approved, the upgrade details are added to `upgrade-info.json`. Cosmovisor downloads and installs the upgrade.

3. **Scalability Considerations**:
    - The binaries must be hosted in a manner that supports a high volume of simultaneous downloads during the upgrade process.

---

## Upgrade Workflow

1. **Chain-Specific Upgrade Handlers**:
    - Add the actual upgrade handler to the chain to manage data migrations or other state updates.
    - Look in the `inference-chain/app/upgrades` folder and the `inference-chain/app/upgrades.go` file for an example
    - Key elements include:
        - Constants defining the upgrade name (in `constants.go`).
        - A `CreateUpgradeHandler` function.
        - Invocation in `setupUpgradeHandlers` (found in `upgrades.go`).

2. **Build the Binaries**:
    - Generate and publish binaries with the `build-for-upgrade` Makefile target.
    - Ensure appropriate versioning and SHAs.
    - Document the changes clearly.

3. **Submit Governance Proposal**:
    - Include upgrade details such as binaries, SHAs, and hosting URLs.
      Example command line to submit:
   ```
   inferenced tx upgrade software-upgrade v0.0.2test --title v0.0.2test --upgrade-height 74 --upgrade-info {"binaries":{"linux/amd64":"http://binary-server/v2/inferenced/inferenced.zip?checksum=sha256:32620280f4b6abe013e97a521ae48f1c6915c78a51cc6661c51c429951fe6032"},"api_binaries":{"linux/amd64":"http://binary-server/v2/dapi/decentralized-api.zip?checksum=sha256:06ba4bb537ce5e139edbd4ffdac5d68acc5e5bc1da89b4989f12c5fe1919118b"}} --summary For testing --deposit 100000icoin --from cosmos1jz6smxmljlr4yqymf7lw5qcfuvw700w2g663vp --keyring-backend test --chain-id=prod-sim --keyring-dir=/root/.inference --yes --broadcast-mode sync --output json
   ```

4. **Promote the Proposal**:
    - Allow stakeholders to test the binaries.
    - Provide documentation and tools for validating and describing the changes.

5. **Monitor the Rollout**:
    - Upon governance approval, the upgrade will be scheduled for the `upgrade-height`
    - Monitor the network disruption during rollout as nodes update simultaneously and download the binaries.

---

## Testing the Upgrade Mechanism (not specific upgrades)
Testing is semi-automated, with plans for further automation.

### Steps:
1. Update `constants.go`:
    - Set the version to `v0.0.1-test`.
2. Build and launch the app. (locally, as you would for Testermint)
3. Update the version to `v0.0.2-test` in `constants.go`.
4. Rebuild using `build-for-upgrade`.
    - Outputs are stored in the correct directory, and SHAs are output during the build.
5. Run `submit upgrade` test:
    - Change the SHAs in the Testermint test.
    - Run tests to ensure successful version upgrades.

---

## Key Notes
- **Upgrade Handlers**: Each upgrade requires a tailored handler for any necessary migrations.
- **Documentation**: Ensure clear communication of changes and steps for stakeholders.
- **Scalability**: Account for possible high traffic during binary downloads.

