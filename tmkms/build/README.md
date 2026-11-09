# tmkms build inputs

`tmkms-0.14.0.Cargo.lock` is the lockfile both `docker/Dockerfile_*` images build
tmkms 0.14.0 with (`cargo build --release --locked --features softsign`), instead
of the upstream lock that `cargo install --locked` would use.

## Why 0.14.0

tmkms 0.15.0 computes the vote-extension signature but encodes `SignedVoteResponse`
with `cometbft.types.v1beta1.Vote`, which has no `extension` / `extension_signature`
fields, so CometBFT 0.38 receives an empty signature and the node panics
(iqlusioninc/tmkms#1174; reproduced on a testermint cluster). 0.14.0 encodes with
the v0.38 `Vote` (tags 9/10). See `devshard/docs/proposals/block-bls-beacon-vote-extensions-plan.md` (F7).

## Provenance

- Source: `https://static.crates.io/crates/tmkms/tmkms-0.14.0.crate`, sha256
  `4c61273ef1e4290b4fb055e0b9cfb006c567740446f5cbe7959ed6dba0cfb77a`
  (matches the crates.io index entry; verified in the Dockerfiles).
- Lock: upstream 0.14.0 `Cargo.lock` plus semver-compatible updates only:

  | Crate | Upstream lock | Here | Reason |
  |---|---|---|---|
  | `bytes` | 1.6.0 (yanked) | 1.12.1 | RUSTSEC-2026-0007 |
  | `time` (+ `-core`, `-macros`, `deranged`) | 0.3.34 | 0.3.47 | RUSTSEC-2026-0009; also compiles on Rust ≥ 1.80 (old one fails with E0282) |
  | `tracing-subscriber` | 0.3.18 | 0.3.20 | RUSTSEC-2025-0055 |
  | `anyhow` | 1.0.81 | 1.0.104 | RUSTSEC-2026-0190 (unsound) |
  | `keccak` | 0.1.5 (yanked) | 0.1.6 | RUSTSEC-2026-0012 (unsound) |
  | `futures-*` | 0.3.30 (yanked) | 0.3.34 | yanked |
  | `spin` | 0.9.8 (yanked) | 0.9.9 | yanked |
  | `curve25519-dalek` | 4.1.2 | 4.1.3 | RUSTSEC-2024-0344 (not compiled for softsign) |
  | `h2` | 0.3.25 | 0.3.27 | RUSTSEC-2024-0332 (not compiled for softsign) |
  | `openssl` | 0.10.64 | 0.10.73 | RUSTSEC-2024-0357, -2025-0004, -2025-0022 (not compiled for softsign) |
  | `tokio`, `rand`, `libc` | 1.36.0, 0.8.5, 0.2.153 | 1.47.1, 0.8.8, 0.2.191 | unsound advisories / pulled along |

## Accepted residuals (`cargo audit` allowlist in CI)

Fixes need a major version bump of an upstream dependency; none is exploitable in
our configuration.

| Advisory | Crate | Compiled for softsign? | Why accepted |
|---|---|---|---|
| RUSTSEC-2026-0387 timing variability | `crypto-bigint` 0.5.5 | yes | Reached only via `k256`/`ecdsa` (secp256k1). Our consensus key is Ed25519; secp256k1 signing is not configured |
| RUSTSEC-2024-0421 Punycode | `idna` 0.5.0 / 0.1.5 | 0.5.0 yes | Via `url` in `tendermint-config`: parses addresses from the operator's own `tmkms.toml` only |
| RUSTSEC-2026-0258 empty DATA frames | `h2` 0.3.27 | no | Fixed only in 0.4.x; used by optional HSM backends we do not build |
| RUSTSEC-2026-0386 `AsyncFd` I/O safety (unsound) | `tokio` 1.47.1 | no | Fix is ≥ 1.51.6; not compiled for softsign |
| RUSTSEC-2025-0056, -2024-0436 unmaintained | `adler`, `paste` | yes | Unmaintained, no known vulnerability; `paste` is compile-time only |
| RUSTSEC-2020-0036, -2019-0036 | `failure` 0.1.8 | no | Optional Ledger backend only |

## Regenerating

```bash
# from a scratch dir with the 0.14.0 crate extracted to ./tmkms-0.14.0
cp <repo>/tmkms/build/tmkms-0.14.0.Cargo.lock tmkms-0.14.0/Cargo.lock
docker run --rm -v "$PWD/tmkms-0.14.0:/src" -w /src rust:1.91-slim-bookworm \
  sh -c 'apt-get update -qq && apt-get install -y -qq pkg-config libusb-1.0-0-dev &&
         cargo update -p <crate> --precise <version> &&
         cargo build --release --locked --features softsign'
cp tmkms-0.14.0/Cargo.lock <repo>/tmkms/build/tmkms-0.14.0.Cargo.lock
```

Use `--precise`: plain `cargo update -p` keeps versions compatible with tmkms's
declared `rust-version` and stops short of some fixes. After any change, rebuild
the image and rerun the tmkms cases of `testermint` `BlockBeaconTests`.
