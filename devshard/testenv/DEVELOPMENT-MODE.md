# Development mode

Placeholder. The canonical spec for the dev overlay (air live-reload + dlv
remote debugging + `docker-compose.dev.yml`) lives in
[`../docs/testenv.md`](../docs/testenv.md) §Phase 12.

This file will be populated once Phase 12 is implemented. Target content:

- Quick start (`make dev-up`, attaching dlv, rebuild cycle).
- Per-service air config matrix (plain vs debug).
- VS Code / GoLand launch configurations (see `vscode-launch.json`).
- Troubleshooting: clearing the build cache, stuck dlv sessions,
  rebuilding the toolchain image.
