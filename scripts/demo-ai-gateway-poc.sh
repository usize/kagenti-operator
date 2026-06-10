#!/usr/bin/env bash
# ============================================================================
# AI GATEWAY POC DEMO
# ============================================================================
# Deploys the full AI Gateway PoC on a Kind cluster and validates:
#   1. Ollama LLM routing through Envoy AI Gateway
#   2. SPIFFE mTLS enforcement (trusted vs untrusted workloads)
#   3. Local rate limiting
#
# Prerequisites: kind, kubectl, helm, docker
# Usage:
#   scripts/demo-ai-gateway-poc.sh
# ============================================================================

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
ZOO_ROOT="$(cd "$REPO_ROOT/.." && pwd)"
MANIFESTS_DIR="$SCRIPT_DIR/demo-ai-gateway-poc/manifests"
LOG_DIR="/tmp/kagenti/ai-gateway-poc"
DEMO_NS="team1"
OPERATOR_NS="kagenti-system"

mkdir -p "$LOG_DIR"

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

log()  { echo -e "${BLUE}[$(date +%H:%M:%S)]${NC} $*"; }
ok()   { echo -e "${GREEN}[OK]${NC} $*"; }
warn() { echo -e "${YELLOW}[WARN]${NC} $*"; }
fail() { echo -e "${RED}[FAIL]${NC} $*"; }

# ── Phase 1: Prerequisites ──────────────────────────────────────────────────
log "Phase 1: Checking prerequisites"
for cmd in kind kubectl helm docker; do
  if ! command -v "$cmd" &>/dev/null; then
    fail "$cmd is required but not installed"
    exit 1
  fi
done
ok "All prerequisites available"

# ── Phase 2: Platform setup ─────────────────────────────────────────────────
log "Phase 2: Setting up Kagenti platform with SPIRE"
INSTALLER="$ZOO_ROOT/kagenti/scripts/kind/setup-kagenti.sh"
if [[ ! -f "$INSTALLER" ]]; then
  fail "Platform installer not found at $INSTALLER"
  fail "Ensure the kagenti repo is cloned alongside kagenti-operator in the parent directory"
  exit 1
fi

"$INSTALLER" --with-spire > "$LOG_DIR/01-platform-setup.log" 2>&1 || {
  fail "Platform setup failed (see $LOG_DIR/01-platform-setup.log)"
  exit 1
}
ok "Platform deployed with SPIRE"

# ── Phase 3: Install Envoy Gateway + AI Gateway ────────────────────────────
log "Phase 3: Installing Envoy Gateway + AI Gateway"

# Envoy Gateway v1.7.0
helm upgrade --install eg \
  oci://docker.io/envoyproxy/gateway-helm \
  --version v1.7.0 \
  -n envoy-gateway-system --create-namespace \
  --set config.envoyGateway.extensionApis.enableBackend=true \
  --set config.envoyGateway.extensionManager.hooks.xdsTranslator.post='{Translation,Cluster,Route}' \
  --set config.envoyGateway.extensionManager.service.fqdn.hostname=ai-gateway-controller.envoy-ai-gateway-system.svc.cluster.local \
  --set config.envoyGateway.extensionManager.service.fqdn.port=1063 \
  --wait > "$LOG_DIR/02-envoy-gateway.log" 2>&1
ok "Envoy Gateway v1.7.0 installed"

# AI Gateway CRDs v0.6.0
helm upgrade --install ai-gateway-crds \
  oci://docker.io/envoyproxy/ai-gateway-crds-helm \
  --version v0.6.0 \
  -n envoy-ai-gateway-system --create-namespace \
  > "$LOG_DIR/03-ai-gateway-crds.log" 2>&1
ok "AI Gateway CRDs v0.6.0 installed"

# AI Gateway controller v0.6.0
helm upgrade --install ai-gateway \
  oci://docker.io/envoyproxy/ai-gateway-helm \
  --version v0.6.0 \
  -n envoy-ai-gateway-system \
  --wait > "$LOG_DIR/04-ai-gateway-ctrl.log" 2>&1
ok "AI Gateway controller v0.6.0 installed"

# GatewayClass
kubectl apply -f - <<'EOF'
apiVersion: gateway.networking.k8s.io/v1
kind: GatewayClass
metadata:
  name: eg
spec:
  controllerName: gateway.envoyproxy.io/gatewayclass-controller
EOF
ok "GatewayClass 'eg' created"

# ── Phase 4: Deploy Ollama ──────────────────────────────────────────────────
log "Phase 4: Deploying Ollama in-cluster"
kubectl create namespace "$DEMO_NS" --dry-run=client -o yaml | kubectl apply -f -
kubectl apply -f "$MANIFESTS_DIR/ollama.yaml"
kubectl rollout status deployment/ollama -n "$DEMO_NS" --timeout=120s > "$LOG_DIR/05-ollama-deploy.log" 2>&1
ok "Ollama deployment ready"

log "Pulling qwen2.5:3b model (this may take a few minutes)"
kubectl exec -n "$DEMO_NS" deploy/ollama -- ollama pull qwen2.5:3b > "$LOG_DIR/06-ollama-pull.log" 2>&1
ok "Model qwen2.5:3b pulled"

# ── Phase 5: Build and deploy operator ──────────────────────────────────────
log "Phase 5: Building operator with AI Gateway support"
OPERATOR_DIR="$REPO_ROOT/kagenti-operator"

# Build using ko
cd "$OPERATOR_DIR"
KO_DOCKER_REPO=ko.local ko build ./cmd/... --bare --platform=linux/$(go env GOARCH) \
  -t ai-gateway-poc > "$LOG_DIR/07-operator-build.log" 2>&1
OPERATOR_IMAGE="ko.local/cmd:ai-gateway-poc"

# Load into Kind
kind load docker-image "$OPERATOR_IMAGE" --name kagenti > "$LOG_DIR/08-kind-load.log" 2>&1
cd "$REPO_ROOT"
ok "Operator image built and loaded"

# Install operator with AI Gateway enabled
helm upgrade --install kagenti-operator \
  "$REPO_ROOT/charts/kagenti-operator" \
  -n "$OPERATOR_NS" \
  --set controllerManager.container.image.repository=ko.local/cmd \
  --set controllerManager.container.image.tag=ai-gateway-poc \
  --set aiGateway.enable=true \
  --reuse-values \
  --wait > "$LOG_DIR/09-operator-install.log" 2>&1
ok "Operator deployed with --enable-ai-gateway"

# ── Phase 6: Create trust bundle ────────────────────────────────────────────
log "Phase 6: Creating SPIRE trust bundle in demo namespace"

# Copy the SPIRE trust bundle from spire-system to team1
BUNDLE_DATA=$(kubectl get configmap spire-bundle -n spire-system -o jsonpath='{.data.bundle\.spiffe}')
kubectl create configmap spire-bundle -n "$DEMO_NS" \
  --from-literal="bundle.spiffe=$BUNDLE_DATA" \
  --dry-run=client -o yaml | kubectl apply -f -
ok "Trust bundle ConfigMap synced to $DEMO_NS"

# ── Phase 7: Apply AI Gateway policies ──────────────────────────────────────
log "Phase 7: Applying Gateway + AIRoutingPolicy + AIAccessPolicy"
kubectl apply -f "$MANIFESTS_DIR/gateway.yaml"
kubectl apply -f "$MANIFESTS_DIR/airoutingpolicy.yaml"
kubectl apply -f "$MANIFESTS_DIR/aiaccesspolicy.yaml"

# Wait for controller to reconcile
log "Waiting for controllers to reconcile..."
sleep 15

# Verify generated resources
log "Verifying generated resources"
EXPECTED_RESOURCES=(
  "backend/ai-gateway-routing-ollama"
  "aiservicebackend/ai-gateway-routing-ollama"
  "aigatewayroute/ai-gateway-routing"
  "secret/ai-gateway-access-mtls-ca"
  "secret/ai-gateway-access-mtls-server"
)

ALL_FOUND=true
for res in "${EXPECTED_RESOURCES[@]}"; do
  if kubectl get "$res" -n "$DEMO_NS" &>/dev/null; then
    ok "  $res created"
  else
    fail "  $res NOT found"
    ALL_FOUND=false
  fi
done

if [[ "$ALL_FOUND" != "true" ]]; then
  warn "Some resources missing. Check operator logs:"
  warn "  kubectl logs -n $OPERATOR_NS deploy/kagenti-controller-manager -c manager --tail=50"
fi

# ── Phase 8: Deploy test pods ───────────────────────────────────────────────
log "Phase 8: Deploying test pods"
kubectl apply -f "$MANIFESTS_DIR/curl-trusted.yaml"
kubectl apply -f "$MANIFESTS_DIR/curl-untrusted.yaml"

kubectl wait --for=condition=Ready pod/curl-trusted -n "$DEMO_NS" --timeout=120s > /dev/null 2>&1
kubectl wait --for=condition=Ready pod/curl-untrusted -n "$DEMO_NS" --timeout=60s > /dev/null 2>&1
ok "Test pods ready"

# ── Phase 9: Test mTLS enforcement ─────────────────────────────────────────
log "Phase 9: Testing mTLS enforcement"

# Discover the gateway service
GW_SVC=$(kubectl get svc -n envoy-gateway-system -l gateway.envoyproxy.io/owning-gateway-name=ai-gateway -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || echo "")
if [[ -z "$GW_SVC" ]]; then
  warn "Gateway service not found (Gateway may not be programmed yet)"
  warn "Skipping inference tests"
else
  GW_ENDPOINT="https://$GW_SVC.envoy-gateway-system.svc:8443"
  CHAT_BODY='{"model":"qwen2.5:3b","messages":[{"role":"user","content":"Say hello in one word"}]}'

  # Test 1: Trusted pod with client certs
  log "Test 1: Trusted pod (with SPIFFE certs) → Gateway"
  TRUSTED_STATUS=$(kubectl exec -n "$DEMO_NS" curl-trusted -- \
    curl -s -o /dev/null -w '%{http_code}' \
    --cert /spiffe-certs/tls.crt \
    --key /spiffe-certs/tls.key \
    --cacert /spiffe-certs/ca.crt \
    -X POST "$GW_ENDPOINT/v1/chat/completions" \
    -H "Content-Type: application/json" \
    -d "$CHAT_BODY" 2>/dev/null || echo "000")

  if [[ "$TRUSTED_STATUS" == "200" ]]; then
    ok "  Trusted pod: HTTP $TRUSTED_STATUS (inference succeeded)"
  else
    warn "  Trusted pod: HTTP $TRUSTED_STATUS (expected 200)"
  fi

  # Test 2: Untrusted pod without client certs
  log "Test 2: Untrusted pod (no certs) → Gateway"
  UNTRUSTED_STATUS=$(kubectl exec -n "$DEMO_NS" curl-untrusted -- \
    curl -s -o /dev/null -w '%{http_code}' \
    --insecure \
    -X POST "$GW_ENDPOINT/v1/chat/completions" \
    -H "Content-Type: application/json" \
    -d "$CHAT_BODY" --max-time 5 2>/dev/null || echo "000")

  if [[ "$UNTRUSTED_STATUS" == "000" ]]; then
    ok "  Untrusted pod: Connection rejected (mTLS handshake failed)"
  elif [[ "$UNTRUSTED_STATUS" == "400" || "$UNTRUSTED_STATUS" == "403" ]]; then
    ok "  Untrusted pod: HTTP $UNTRUSTED_STATUS (rejected by gateway)"
  else
    warn "  Untrusted pod: HTTP $UNTRUSTED_STATUS (expected connection rejection)"
  fi

  # Test 3: Rate limiting
  log "Test 3: Rate limiting (5 rpm threshold)"
  RATE_LIMITED=false
  for i in $(seq 1 8); do
    STATUS=$(kubectl exec -n "$DEMO_NS" curl-trusted -- \
      curl -s -o /dev/null -w '%{http_code}' \
      --cert /spiffe-certs/tls.crt \
      --key /spiffe-certs/tls.key \
      --cacert /spiffe-certs/ca.crt \
      -X POST "$GW_ENDPOINT/v1/chat/completions" \
      -H "Content-Type: application/json" \
      -d "$CHAT_BODY" 2>/dev/null || echo "000")
    if [[ "$STATUS" == "429" ]]; then
      ok "  Request $i: HTTP 429 (rate limited after threshold)"
      RATE_LIMITED=true
      break
    fi
  done
  if [[ "$RATE_LIMITED" != "true" ]]; then
    warn "  No 429 received after 8 requests (rate limit may not be active yet)"
  fi
fi

# ── Summary ─────────────────────────────────────────────────────────────────
echo ""
echo "=============================================="
echo "  AI Gateway PoC Demo Complete"
echo "=============================================="
echo ""
echo "Resources created in namespace $DEMO_NS:"
kubectl get gateway,airoutingpolicy,aiaccesspolicy,backend,aiservicebackend,aigatewayroute -n "$DEMO_NS" 2>/dev/null || true
echo ""
echo "Secrets:"
kubectl get secret -n "$DEMO_NS" -l app.kubernetes.io/managed-by=kagenti-operator 2>/dev/null || \
kubectl get secret -n "$DEMO_NS" | grep mtls 2>/dev/null || true
echo ""
echo "Logs: $LOG_DIR/"
echo ""
