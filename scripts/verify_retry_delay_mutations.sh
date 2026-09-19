#!/usr/bin/env bash
#
# Mutation verification for the retry delay mode fix in retrypolicy.
#
# Applies three mutations to retrypolicy/retry.go, one at a time, and asserts that the regression tests in
# retrypolicy/delayconfig_test.go fail for each of them:
#
#   1. WithRandomDelay does not clear previously configured fixed/maxDelay (backoff) state.
#   2. WithDelay keeps previously configured backoff state instead of replacing it.
#   3. Build aliases the builder's config, so later builder configuration rewrites already built policies.
#
# The workspace is restored to its original state when the script exits, regardless of the outcome.

set -uo pipefail

cd "$(dirname "$0")/.."

TARGET="retrypolicy/retry.go"
TEST_RUN='TestDelayMode|TestBuilderSnapshotIsolation|TestBuiltPoliciesConcurrentIsolation'
BACKUP="$(mktemp)"
cp "$TARGET" "$BACKUP"

cleanup() {
  cp "$BACKUP" "$TARGET"
  rm -f "$BACKUP"
}
trap cleanup EXIT

run_tests() {
  go test ./retrypolicy/ -count=1 -run "$TEST_RUN" > /dev/null 2>&1
}

echo "== Baseline: regression tests must pass before mutations are applied =="
if ! run_tests; then
  echo "ERROR: baseline regression tests do not pass; cannot verify mutations"
  exit 1
fi
echo "ok"

status=0
verify() {
  local name="$1"
  if run_tests; then
    echo "MUTATION SURVIVED: $name -- regression tests unexpectedly passed"
    status=1
  else
    echo "detected: $name -- regression tests failed as expected"
  fi
  cp "$BACKUP" "$TARGET"
}

echo "== Mutation 1: random delay does not clear fixed/maxDelay state =="
perl -0pi -e 's/(func \(c \*config\[R\]\) WithRandomDelay\(delayMin time\.Duration, delayMax time\.Duration\) Builder\[R\] \{\n)\tc\.resetDelay\(\)\n/$1/' "$TARGET"
verify "random delay does not clear fixed/maxDelay state"

echo "== Mutation 2: fixed delay keeps previously configured backoff state =="
perl -0pi -e 's/(func \(c \*config\[R\]\) WithDelay\(delay time\.Duration\) Builder\[R\] \{\n)\tc\.resetDelay\(\)\n/$1/' "$TARGET"
verify "fixed delay keeps backoff state"

echo "== Mutation 3: Build aliases builder state instead of snapshotting =="
perl -0pi -e 's/(type retryPolicy\[R any\] struct \{\n)\tconfig\[R\]\n/$1\t*config[R]\n/' "$TARGET"
perl -0pi -e 's/\t\tconfig: \*c\.copy\(\),/\t\tconfig: c,/' "$TARGET"
verify "Build aliases builder state"

if ! cmp -s "$TARGET" "$BACKUP"; then
  echo "ERROR: workspace was not restored cleanly"
  status=1
fi

if [ "$status" -eq 0 ]; then
  echo "All mutations were detected by the regression tests. Workspace restored."
fi
exit "$status"
