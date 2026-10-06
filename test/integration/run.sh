#!/bin/sh
set -eu

NAMESPACE="${1:-default}"
RELEASE="${2:-kube-replica-server}"
CERT_DIR="${3:-certs}"

TEST_DEPLOYMENT="${TEST_DEPLOYMENT:-replica-test}"
KUBECTL_CONTEXT="${KUBECTL_CONTEXT:-kind-kube-replica-server}"

PORT="${PORT:-18443}"
HEALTH_PORT="${HEALTH_PORT:-18081}"

API_URL="https://localhost:$PORT"
HEALTH_URL="http://localhost:$HEALTH_PORT"

API_LOG="$(mktemp)"
HEALTH_LOG="$(mktemp)"

PF_PID=""
HEALTH_PF_PID=""
FIXTURE_INITIALIZED=false

# Ensure every kubectl command uses the intended cluster.
kubectl() {
  command kubectl --context "$KUBECTL_CONTEXT" "$@"
}

# Clean up integration test resources on exit or interruption.
# Restore the test Deployment, terminate background port-forwards,
# and print diagnostic logs if the test failed. Preserve the original
# exit status so cleanup does not mask a test failure.
cleanup() {
  status=$?

  trap - EXIT INT TERM

  # Restore the test fixture even if a test failed midway.
  if [ "$FIXTURE_INITIALIZED" = true ]; then
    kubectl -n "$NAMESPACE" scale \
      "deployment/$TEST_DEPLOYMENT" \
      --replicas=2 >/dev/null 2>&1 || true
  fi

  # Stop background port-forward processes.
  if [ -n "$PF_PID" ]; then
    kill "$PF_PID" 2>/dev/null || true
    wait "$PF_PID" 2>/dev/null || true
  fi

  if [ -n "$HEALTH_PF_PID" ]; then
    kill "$HEALTH_PF_PID" 2>/dev/null || true
    wait "$HEALTH_PF_PID" 2>/dev/null || true
  fi

  # Print diagnostic information when something fails.
  if [ "$status" -ne 0 ]; then
    echo "" >&2
    echo "--- API port-forward log ---" >&2
    cat "$API_LOG" >&2

    echo "--- Health port-forward log ---" >&2
    cat "$HEALTH_LOG" >&2
  fi

  rm -f "$API_LOG" "$HEALTH_LOG"

  exit "$status"
}

trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

# Report a test failure to stderr and terminate the script
# with a nonzero exit status.
fail() {
  echo "FAIL: $*" >&2
  exit 1
}

# Report a successful test assertion to stdout.
pass() {
  echo "PASS: $*"
}

# Execute an authenticated mTLS request.
curl_mtls() {
  curl --fail --silent --show-error \
    --connect-timeout 3 \
    --max-time 10 \
    --cacert "$CERT_DIR/ca.crt" \
    --cert "$CERT_DIR/client.crt" \
    --key "$CERT_DIR/client.key" \
    "$@"
}

# Retrieve an HTTP status without treating expected 4xx
# responses as curl failures.
http_code() {
  curl --silent --show-error \
    --connect-timeout 3 \
    --max-time 10 \
    --output /dev/null \
    --write-out '%{http_code}' \
    --cacert "$CERT_DIR/ca.crt" \
    --cert "$CERT_DIR/client.crt" \
    --key "$CERT_DIR/client.key" \
    "$@"
}

# Assert that the actual HTTP status matches the expected status.
# Fail the test with a descriptive message if they differ.
assert_status() {
  expected="$1"
  actual="$2"
  description="$3"

  if [ "$actual" != "$expected" ]; then
    fail "$description: expected HTTP $expected, got $actual"
  fi
}

# Check whether a JSON response contains the expected
# replica count, allowing optional whitespace.
has_replicas() {
  expected="$1"
  grep -Eq \
    "\"replicas\"[[:space:]]*:[[:space:]]*$expected([,}])"
}

# The API reads from an informer-backed cache, so a successful
# write may not be immediately visible through GET.
wait_for_replicas() {
  expected="$1"

  for _ in $(seq 1 30); do
    if response="$(curl_mtls \
      "$API_URL/api/v1/deployments/$TEST_DEPLOYMENT/replicas" \
      2>/dev/null)"; then

      if printf '%s\n' "$response" |
        has_replicas "$expected"; then
        return 0
      fi
    fi

    sleep 0.25
  done

  if ! kill -0 "$PF_PID" 2>/dev/null; then
    fail "API port-forward terminated while waiting for replicas=$expected"
  fi

  fail "Informer cache did not converge to replicas=$expected"
}

echo "Starting Replica Server integration tests..."
echo "Context:   $KUBECTL_CONTEXT"
echo "Namespace: $NAMESPACE"
echo "Release:   $RELEASE"

# ------------------------------------------------------------
# 1. Validate prerequisites
# ------------------------------------------------------------

echo "Checking prerequisites..."

for file in \
  "$CERT_DIR/ca.crt" \
  "$CERT_DIR/client.crt" \
  "$CERT_DIR/client.key"
do
  [ -f "$file" ] ||
    fail "Missing certificate file: $file"
done

kubectl -n "$NAMESPACE" get \
  "deployment/$RELEASE" >/dev/null ||
  fail "Replica Server Deployment not found"

kubectl -n "$NAMESPACE" get \
  "service/$RELEASE" >/dev/null ||
  fail "Replica Server Service not found"

kubectl -n "$NAMESPACE" get \
  "deployment/$TEST_DEPLOYMENT" >/dev/null ||
  fail "Test Deployment not found: $TEST_DEPLOYMENT"

pass "prerequisites"

# ------------------------------------------------------------
# 2. Reset the test fixture
# ------------------------------------------------------------

echo "Initializing test Deployment..."

kubectl -n "$NAMESPACE" scale \
  "deployment/$TEST_DEPLOYMENT" \
  --replicas=2 >/dev/null ||
  fail "Failed to initialize test Deployment"

FIXTURE_INITIALIZED=true

pass "test fixture initialized"

# ------------------------------------------------------------
# 3. Start port-forwards
# ------------------------------------------------------------

echo "Starting port-forwards..."

kubectl -n "$NAMESPACE" port-forward \
  "service/$RELEASE" \
  "$PORT:8443" >"$API_LOG" 2>&1 &

PF_PID=$!

kubectl -n "$NAMESPACE" port-forward \
  "deployment/$RELEASE" \
  "$HEALTH_PORT:8081" >"$HEALTH_LOG" 2>&1 &

HEALTH_PF_PID=$!

# ------------------------------------------------------------
# 4. Test liveness and readiness
# ------------------------------------------------------------

echo "Waiting for health endpoints..."

health_ready=false

for _ in $(seq 1 30); do
  if curl --fail --silent \
    --connect-timeout 2 \
    --max-time 3 \
    "$HEALTH_URL/livez" >/dev/null 2>&1 &&
    curl --fail --silent \
    --connect-timeout 2 \
    --max-time 3 \
    "$HEALTH_URL/readyz" >/dev/null 2>&1; then

    health_ready=true
    break
  fi

  sleep 1
done

[ "$health_ready" = true ] ||
  fail "Health endpoints did not become ready"

pass "liveness and readiness"

# ------------------------------------------------------------
# 5. Wait for the mTLS API
# ------------------------------------------------------------

echo "Waiting for mTLS API..."

api_ready=false

for _ in $(seq 1 30); do
  if curl_mtls \
    "$API_URL/api/v1/deployments" >/dev/null 2>&1; then

    api_ready=true
    break
  fi

  sleep 1
done

[ "$api_ready" = true ] ||
  fail "mTLS API did not become available"

pass "mTLS API accessible"

# ------------------------------------------------------------
# 6. Verify initial cached replica count
# ------------------------------------------------------------

echo "Checking initial replica count..."

wait_for_replicas 2

pass "initial replica count is 2"

# ------------------------------------------------------------
# 7. Update replicas through the API
# ------------------------------------------------------------

echo "Updating replicas from 2 to 3..."

response="$(curl_mtls \
  -X PUT \
  -H 'Content-Type: application/json' \
  -d '{"replicas":3}' \
  "$API_URL/api/v1/deployments/$TEST_DEPLOYMENT/replicas")" ||
  fail "PUT replicas request failed"

printf '%s\n' "$response" |
  has_replicas 3 ||
  fail "PUT response did not contain replicas=3: $response"

pass "replica update"

# ------------------------------------------------------------
# 8. Verify Kubernetes desired replicas after PUT
# ------------------------------------------------------------

echo "Checking Kubernetes desired replica count..."

actual="$(kubectl -n "$NAMESPACE" \
  get deployment "$TEST_DEPLOYMENT" \
  -o jsonpath='{.spec.replicas}')" ||
  fail "Failed to retrieve Kubernetes Deployment"

if [ "$actual" != "3" ]; then
  fail "Expected Kubernetes desired replicas=3, got $actual"
fi

pass "Kubernetes desired replica count updated"

# ------------------------------------------------------------
# 9. Verify informer cache convergence
# ------------------------------------------------------------

echo "Waiting for informer cache convergence..."

wait_for_replicas 3

pass "informer cache converged to 3 replicas"

# ------------------------------------------------------------
# 10. Verify deployment listing
# ------------------------------------------------------------

echo "Checking Deployment listing..."

response="$(curl_mtls \
  "$API_URL/api/v1/deployments")" ||
  fail "GET deployments request failed"

printf '%s\n' "$response" |
  grep -Eq "\"name\"[[:space:]]*:[[:space:]]*\"$TEST_DEPLOYMENT\"" ||
  fail "Deployment listing does not contain $TEST_DEPLOYMENT"

pass "Deployment listing"

# ------------------------------------------------------------
# 11. Verify unknown Deployment returns HTTP 404
# ------------------------------------------------------------

echo "Checking unknown Deployment handling..."

code="$(http_code \
  "$API_URL/api/v1/deployments/does-not-exist/replicas")" ||
  fail "Unknown Deployment request failed"

assert_status 404 "$code" "Unknown Deployment"

pass "unknown Deployment returns HTTP 404"

# ------------------------------------------------------------
# 12. Verify malformed JSON returns HTTP 400
# ------------------------------------------------------------

echo "Checking malformed JSON..."

code="$(http_code \
  -X PUT \
  -H 'Content-Type: application/json' \
  -d '{"replicas":' \
  "$API_URL/api/v1/deployments/$TEST_DEPLOYMENT/replicas")" ||
  fail "Malformed JSON request failed"

assert_status 400 "$code" "Malformed JSON"

pass "malformed JSON rejected"

# ------------------------------------------------------------
# 13. Verify replica count validation
# ------------------------------------------------------------

echo "Checking replica bounds..."

code="$(http_code \
  -X PUT \
  -H 'Content-Type: application/json' \
  -d '{"replicas":101}' \
  "$API_URL/api/v1/deployments/$TEST_DEPLOYMENT/replicas")" ||
  fail "Upper-bound validation request failed"

assert_status 400 "$code" "Replicas above maximum"

code="$(http_code \
  -X PUT \
  -H 'Content-Type: application/json' \
  -d '{"replicas":-1}' \
  "$API_URL/api/v1/deployments/$TEST_DEPLOYMENT/replicas")" ||
  fail "Negative replica validation request failed"

assert_status 400 "$code" "Negative replicas"

pass "replica bounds validation"

# ------------------------------------------------------------
# 14. Verify mTLS rejects missing client certificates
# ------------------------------------------------------------

echo "Checking mTLS enforcement..."

if curl --fail --silent \
  --connect-timeout 3 \
  --max-time 10 \
  --cacert "$CERT_DIR/ca.crt" \
  "$API_URL/api/v1/deployments" >/dev/null 2>&1; then

  fail "API accepted a request without a client certificate"
fi

pass "missing client certificate rejected"

# ------------------------------------------------------------
# 15. Restore the test fixture
# ------------------------------------------------------------

echo "Restoring test Deployment..."

kubectl -n "$NAMESPACE" scale \
  "deployment/$TEST_DEPLOYMENT" \
  --replicas=2 >/dev/null ||
  fail "Failed to restore test Deployment"

actual="$(kubectl -n "$NAMESPACE" \
  get deployment "$TEST_DEPLOYMENT" \
  -o jsonpath='{.spec.replicas}')" ||
  fail "Failed to verify restored Deployment"

if [ "$actual" != "2" ]; then
  fail "Expected restored Kubernetes replicas=2, got $actual"
fi

FIXTURE_INITIALIZED=false

pass "test fixture restored"

echo ""
echo "All integration tests passed."
