#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
source "$SCRIPT_DIR/helpers.sh"

NO_TEARDOWN=false
if [ "${1:-}" = "--no-teardown" ]; then
    NO_TEARDOWN=true
fi

passed=0
failed=0
failed_tests=""

cleanup() {
    if [ "$failed" -gt 0 ]; then
        info "dumping debug info..."
        dump_debug
    fi
    if [ "$NO_TEARDOWN" = true ]; then
        info "skipping teardown (--no-teardown). cluster '$CLUSTER_NAME' is still running."
        info "to delete: kind delete cluster --name $CLUSTER_NAME"
    else
        info "running teardown..."
        "$SCRIPT_DIR/teardown.sh" || true
    fi
}
trap cleanup EXIT

info "setting up cluster..."
"$SCRIPT_DIR/setup.sh"

for test_script in "$SCRIPT_DIR/tests"/*.sh; do
    test_name="$(basename "$test_script")"
    info "running $test_name"
    if bash "$test_script"; then
        pass "$test_name"
        passed=$((passed + 1))
    else
        fail "$test_name"
        failed=$((failed + 1))
        failed_tests="$failed_tests $test_name"
    fi
done

echo ""
info "results: $passed passed, $failed failed"
if [ "$failed" -gt 0 ]; then
    fail "failed tests:$failed_tests"
    exit 1
fi
pass "all tests passed"
