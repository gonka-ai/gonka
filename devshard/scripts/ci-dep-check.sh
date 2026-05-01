#!/usr/bin/env bash
# devshard/docs/testenv.md §8.4 — dependency + API surface invariants (PR CI).
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"
REPO_ROOT="$(cd "$ROOT/.." && pwd)"
DAPI="$REPO_ROOT/decentralized-api"

fail() { echo "ci-dep-check: $*" >&2; exit 1; }

# 1. blockoracle must not depend on testenv
if go list -deps -test=false ./blockoracle/... 2>/dev/null | grep -q 'devshard/testenv'; then
  fail "rule 1: blockoracle must not depend on devshard/testenv"
fi

# 2. blockoracle must not depend on decentralized-api
if go list -deps -test=false ./blockoracle/... 2>/dev/null | grep -q 'decentralized-api'; then
  fail "rule 2: blockoracle must not depend on decentralized-api"
fi

# 3. mockdapi must depend on blockoracle/client
if ! go list -deps -test=false ./testenv/mockdapi/... 2>/dev/null | grep -q 'devshard/blockoracle/client'; then
  fail "rule 3: testenv/mockdapi must depend on devshard/blockoracle/client"
fi

# 4. mockdapi must not depend on decentralized-api
if go list -deps -test=false ./testenv/mockdapi/... 2>/dev/null | grep -q 'decentralized-api'; then
  fail "rule 4: testenv/mockdapi must not depend on decentralized-api"
fi

# 5. BlockOracle method set (golden) — any change is an intentional review point.
if ! go test -count=1 -run='^TestBlockOracleInterfaceGolden$' ./blockoracle; then
  fail "rule 5: update blockoracle testdata or revert BlockOracle interface"
fi

# 6. decentralized-api must still compile a thin reference to observer.NewTendermint.
if [[ ! -d "$DAPI" ]]; then
  fail "decentralized-api not found at $DAPI (expected next to devshard in repo root)"
fi
( cd "$DAPI" && go build -tags=blockoracle_compile_check -o /dev/null ./internal/devshard/blockoraclecompile/ ) || fail "rule 6: dapi → blockoracle observer edge compile check failed (run: cd devshard && bash scripts/ci-dep-check.sh)"

echo "ci-dep-check: OK (§8.4 rules 1–6)"