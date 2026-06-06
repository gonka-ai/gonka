When making changes under `inference-chain`, run the local gate before handing work off.

Preferred command:

```bash
zsh -lc 'cd /Users/johnlong/dev/gonka/inference-chain && make lint-chain-local'
```

This local gate intentionally excludes Go test files, `testutil`, and generated code/directories such as `api/` and `proto/` so the signal stays focused on handwritten non-test chain code.

The local gate is intentionally split into separate checks so failures stay easy to diagnose:

- `make lint-chain-forbidigo`
- `make lint-chain-govet`
- `make lint-chain-gosimple`
- `make lint-chain-staticcheck`
- `make lint-chain-errcheck`
- `make lint-chain-unused`
- `make lint-chain-ineffassign`
- `make lint-chain-gosec`

Keep the `forbidigo` check separate. It protects chain code from `panic` and `Must*` usage and should not be folded into a generic lint bucket.
