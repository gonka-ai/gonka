// Binary gencompose generates docker-compose.yml from config.yaml so
// adding a host does not require hand-editing compose.
//
// Phase 10; see devshard/docs/testenv.md §Phase 10.
package main

func main() {
	// TODO(phase-10): read config.yaml, render compose templates for:
	//   - mock-chain
	//   - height-sync
	//   - devshardd-testenv-N (one per host slot)
	//   - devshardctl
	//   - observability overlay (opt-in).
}
