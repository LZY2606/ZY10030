#!/usr/bin/env bash
#
# Mutation verification for the retrypolicy delay mode mutual exclusion fix.
#
# Applies each mutant patch under scripts/mutations/ to the working tree, runs the delay mode regression tests
# (which must FAIL against the mutant), then restores the original files. Exits non-zero if any mutant survives
# or if the working tree is not restored cleanly.
#
# Usage (from the repository root): scripts/verify_retrypolicy_mutations.sh
set -u

cd "$(git rev-parse --show-toplevel)"

MUTATED_FILES=(retrypolicy/retry.go retrypolicy/retryexecutor_test.go)
BACKUP_DIR=$(mktemp -d)
STATUS_BEFORE=$(git status --porcelain)

restore() {
  for f in "${MUTATED_FILES[@]}"; do
    if [ -f "$BACKUP_DIR/$(basename "$f")" ]; then
      cp "$BACKUP_DIR/$(basename "$f")" "$f"
    fi
  done
}
trap restore EXIT

for f in "${MUTATED_FILES[@]}"; do
  cp "$f" "$BACKUP_DIR/$(basename "$f")"
done

run_regression_tests() {
  go test ./retrypolicy/ -count=1 \
    -run 'TestDelayModeSwitchingMatrix|TestDelayModeZeroDelay|TestDelayModeBackoffMaxDelay|TestDelayModeJitter|TestBuilderSnapshotIsolation|TestBuiltPoliciesConcurrentUse' || return 1
  go test ./test/ -count=1 -run 'TestRetryPolicyDelayModeSwitching' || return 1
}

echo "== Stage: baseline (unmutated code must pass the regression tests) =="
if ! run_regression_tests; then
  echo "ERROR: regression tests fail against unmutated code" >&2
  exit 1
fi
echo "baseline: OK"

FAILED=0
for patch in scripts/mutations/*.patch; do
  name=$(basename "$patch" .patch)
  echo "== Stage: mutant $name =="
  restore
  if ! git apply "$patch"; then
    echo "ERROR: failed to apply mutant patch $patch" >&2
    FAILED=1
    continue
  fi
  if run_regression_tests; then
    echo "ERROR: mutant $name SURVIVED - regression tests passed but should have failed" >&2
    FAILED=1
  else
    echo "mutant $name: killed (regression tests failed as expected)"
  fi
  restore
  git diff --exit-code --quiet -- "${MUTATED_FILES[@]}" || true
done

restore

STATUS_AFTER=$(git status --porcelain)
if [ "$STATUS_BEFORE" != "$STATUS_AFTER" ]; then
  echo "ERROR: working tree was not restored cleanly" >&2
  exit 1
fi

if [ "$FAILED" -ne 0 ]; then
  echo "FAIL: one or more mutants survived" >&2
  exit 1
fi
echo "OK: all mutants killed and working tree restored"
