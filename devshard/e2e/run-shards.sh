#!/bin/sh
# Compile the e2e test binary once, then run three shards at the same time.
# Each shard has its own 25 minute clock. New tests land in the short shard
# unless they are named in the accounting or timeout lists below.
set -eu

format=testname
case "${DEVSHARD_E2E_DEBUG:-}" in
  1|true|TRUE|yes|YES) format=standard-verbose ;;
esac

mkdir -p build/test-results/e2e
rm -f build/test-results/e2e/junit.xml build/test-results/e2e/junit-*.xml

export PATH="$(go env GOPATH)/bin:${PATH}"
go install "${DEVSHARD_E2E_GOTESTSUM:-gotest.tools/gotestsum@v1.13.0}"
go test -c -o /tmp/e2e.test ./e2e
e2e_pkg=$(go list -f '{{.ImportPath}}' ./e2e)

/tmp/e2e.test -test.list '^Test' > /tmp/e2e-tests.txt

accounting='
TestE2E_AccountingOverscheduledLoserFinishes
TestE2E_AccountingEmptyStreamUsesContentHost
TestE2E_AccountingLiveSendTimeout
TestE2E_AccountingNoReceiptTimeoutBecomesUnfinishedRefused
TestE2E_AccountingAppliedTimeoutCrossCheck
TestE2E_AccountingFocusedGhostRequestNotSent
TestE2E_AccountingGhostStateDivergedNoSendReason
'
timeout='
TestE2E_TimeoutProtocolEndToEnd
TestE2E_ExecutionTimeoutProtocolEndToEnd
TestE2E_AllHostsExecutionHangSurfaces5xx
'

require_listed() {
  if ! grep -qx "$1" /tmp/e2e-tests.txt; then
    echo "e2e shard list is missing $1" >&2
    exit 1
  fi
}

for name in $accounting $timeout; do
  require_listed "$name"
done

: > /tmp/e2e-short.txt
while IFS= read -r name; do
  [ -n "$name" ] || continue
  case "$name" in
    Test*) ;;
    *) continue ;;
  esac
  skip=0
  for slow in $accounting $timeout; do
    if [ "$name" = "$slow" ]; then
      skip=1
      break
    fi
  done
  if [ "$skip" -eq 0 ]; then
    printf '%s\n' "$name" >> /tmp/e2e-short.txt
  fi
done < /tmp/e2e-tests.txt

join_alt() {
  alt=
  while IFS= read -r name; do
    [ -n "$name" ] || continue
    if [ -z "$alt" ]; then
      alt=$name
    else
      alt="${alt}|${name}"
    fi
  done
  if [ -z "$alt" ]; then
    echo "e2e shard has no tests" >&2
    exit 1
  fi
  printf '%s\n' "^(${alt})$"
}

accounting_re=$(printf '%s\n' $accounting | join_alt)
timeout_re=$(printf '%s\n' $timeout | join_alt)
short_re=$(join_alt < /tmp/e2e-short.txt)

run_shard() {
  name=$1
  regex=$2
  echo "=== e2e shard ${name} ==="
  # --raw-command reads go test -json events. The compiled binary prints the
  # text format, so test2json has to sit in front of it.
  gotestsum --format "$format" \
    --junitfile "build/test-results/e2e/junit-${name}.xml" \
    --raw-command -- \
    go tool test2json -t -p "$e2e_pkg" \
    /tmp/e2e.test -test.count=1 -test.v -test.timeout 25m -test.run "$regex"
  echo "=== e2e shard ${name} done ==="
}

run_shard short "$short_re" &
short_pid=$!
run_shard accounting "$accounting_re" &
accounting_pid=$!
run_shard timeout "$timeout_re" &
timeout_pid=$!

fail=0
wait "$short_pid" || fail=1
wait "$accounting_pid" || fail=1
wait "$timeout_pid" || fail=1
exit "$fail"
