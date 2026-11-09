# TMKMS Integration with Our Project

This project integrates **TMKMS** with our system. For the official TMKMS repository, please visit [TMKMS GitHub](https://github.com/iqlusioninc/tmkms).
**Note:** TMKMS does not officially provide Docker images, so we create them ourselves to simplify the setup and deployment process.

### Two Setup Scenarios
There are two different scenarios for running **TMKMS**:

1. **Running with the Network Node**: This scenario is for when a participant is joining the network for the first time and needs to generate a new consensus key.
2. **Migrating an Existing Consensus Key**: In this case, you already have a validator's consensus key, and you want to import it into **TMKMS**.

To support these scenarios, we provide two different Dockerfiles to build two distinct Docker images.

### Build Commands

- **Build Image to Generate a New Validator Private Key:**
  ```bash
  make build-with-keygen
  ```

- **Build Image for Importing Existing Private Validator Key:**
  This scenario is used when migrating an already existing consensus key (`priv_validator_key.json` and `priv_validator_state.json`).
  ```bash
  make build-with-import
  ```

### Building with Latest Tag

To build these images with the `latest` tag, run the above commands and add the `SET_LATEST=1` environment variable:

```bash
export SET_LATEST=1
make build-with-keygen
make build-with-import
```
### Upgrading to vote-extension signing (tmkms 0.14)

The chain will enable CometBFT vote extensions in the `v0.2.18` upgrade. From the
enable height on, every non-nil precommit needs an **extension signature** from
your signer. tmkms 0.12 (image `0.2.16` and older) cannot produce one. A node
signing through it panics in consensus (`non-recoverable error when signing vote`,
`CONSENSUS FAILURE`) and stops validating.

**Upgrade tmkms before the `v0.2.18` proposal.** Doing it early is safe: while vote
extensions are off, the node accepts and discards the extra signature.

The new image runs tmkms **0.14.0**. Do not use 0.15.0: it computes the extension
signature but drops it on the wire (`SignedVoteResponse` is encoded without the
extension fields; see iqlusioninc/tmkms#1174), and the node still panics. On start,
`docker/migrate-toml.sh` updates the `tmkms.toml` already on your volume
(`sign_extensions = true` in `[[chain]]`; `protocol_version` stays `"v0.34"`,
the only value 0.14 accepts — it is the connection protocol, not the CometBFT version). Before the first change it saves
`tmkms.toml.bak.<timestamp>` and `state/priv_validator_state.json.pre-vote-extensions`.

```bash
cp -a .tmkms .tmkms.backup-$(date +%F)   # key, config and double-sign state
docker compose pull tmkms
docker compose up -d tmkms
docker compose logs --tail=200 tmkms
```

Check the logs for:
- `tmkms version: 0.14.0`
- `Migrated /root/.tmkms/tmkms.toml: sign_extensions = true, protocol_version = v0.34` (or `already up to date`)
- recurring `signed vote extension` lines. They appear even before the chain enables
  vote extensions, and they are your readiness signal.

Also confirm your validator still appears in recent block signatures.

**Rollback (only before the enable height).** Reverting the image tag alone takes
your validator **offline**: tmkms 0.12 refuses the migrated config
(`parse error: unknown field 'sign_extensions'`). Make the config 0.12-compatible
first, then switch the image:

```bash
docker compose stop tmkms
# with the new image (runs the bundled script, GNU sed):
docker compose run --rm --entrypoint /root/rollback-toml.sh tmkms /root/.tmkms/tmkms.toml
# or directly on a Linux host:
#   sed -i '/^sign_extensions *=/d; s/^protocol_version *=.*/protocol_version = "v0.34"/' .tmkms/tmkms.toml
# set the previous image tag in docker-compose.yml, then:
docker compose up -d tmkms
docker compose logs --tail=50 tmkms     # expect "tmkms 0.12.0 starting up", no "unknown field"
```

Keep the **current** `state/priv_validator_state.json`. Never restore an older state
file after the new tmkms has signed newer heights. Setting
`TMKMS_SIGN_EXTENSIONS=false` is **not** a rollback: the field stays in the file and
0.12 still rejects it. After the enable height there is no rollback; a signer
without extension signing makes the node panic.

**Image changes in the 0.14 image** (beyond the tmkms version):
- Built from the checksum-pinned 0.14.0 crate with a reviewed lockfile (security patch updates; see `build/README.md`), on pinned base images; the runtime image no longer contains a Rust toolchain (~150 MB instead of ~1.8 GB).
- **tmkms runs as user `tmkms` (uid/gid 10001)**, not root. On every start `init.sh` (still root) migrates the config and then `chown`s the `.tmkms` volume to 10001, so on the host the files become owned by uid 10001. Back up with `sudo` / `cp -a`. Set `TMKMS_RUN_AS=root` to keep the old behaviour.
- New installs get their own KMS identity key (`secrets/kms-identity.key`); older images shipped one shared key. Existing volumes keep theirs. Rotating it is optional, since the node does not pin the KMS identity: stop tmkms, delete the file, start again.

Validators without tmkms (local `priv_validator_key.json`) need no action. If you
use another remote signer, make sure it signs CometBFT 0.38 vote extensions.

Tests: `docker/migrate-toml_test.sh`, `docker/rollback-toml_test.sh` (GNU userland)
and `docker/rollback-smoke_test.sh` (Docker, runs the real 0.12 image), all in CI.

Override the migrated values with `TMKMS_SIGN_EXTENSIONS` (`true`|`false`) and
`TMKMS_PROTOCOL_VERSION` (`v0.34`|`v0.38`; keep `v0.34` for tmkms 0.14). Run the migration tests with:
`docker run --rm -v "$PWD/tmkms:/t:ro" debian:bookworm-slim sh /t/docker/migrate-toml_test.sh`.
