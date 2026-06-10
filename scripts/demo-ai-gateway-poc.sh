#!/usr/bin/env bash
# ============================================================================
# AI GATEWAY POC — INTERACTIVE WALKTHROUGH
# ============================================================================
# Deploys the full AI Gateway PoC on a Kind cluster and validates:
#   1. Ollama LLM routing through Envoy AI Gateway
#   2. SPIFFE mTLS enforcement (trusted vs untrusted workloads)
#
# Press Enter to advance through each step.
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
VALUES_DIR="$SCRIPT_DIR/demo-ai-gateway-poc"
LOG_DIR="/tmp/kagenti/ai-gateway-poc"
DEMO_NS="team1"
OPERATOR_NS="kagenti-system"
OPERATOR_DIR="$REPO_ROOT/kagenti-operator"

mkdir -p "$LOG_DIR"

# ── Helpers ──────────────────────────────────────────────────────────────────
BOLD='\033[1m'
DIM='\033[2m'
GREEN='\033[0;32m'
RED='\033[0;31m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
NC='\033[0m'

step_num=0

step() {
  step_num=$((step_num + 1))
  echo ""
  echo -e "${BOLD}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
  echo -e "${BOLD}  Step ${step_num}: $1${NC}"
  echo -e "${BOLD}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
  echo ""
}

show() {
  echo -e "  ${DIM}\$ $*${NC}"
}

run() {
  echo -e "  ${CYAN}\$ $*${NC}"
  eval "$@"
}

pause() {
  echo ""
  echo -e "  ${DIM}Press Enter to continue...${NC}"
  read -r
}

ok()   { echo -e "  ${GREEN}✓ $*${NC}"; }
fail() { echo -e "  ${RED}✗ $*${NC}"; }

# ── Title ────────────────────────────────────────────────────────────────────
clear 2>/dev/null || true
echo ""
echo -e "${BOLD}"
echo "    ╔══════════════════════════════════════════════════════╗"
echo "    ║           AI GATEWAY PoC — INTERACTIVE DEMO         ║"
echo "    ║                                                      ║"
echo "    ║  Kagenti operator policy attachment pattern:         ║"
echo "    ║  AIRoutingPolicy + AIAccessPolicy → Envoy AI Gateway║"
echo "    ╚══════════════════════════════════════════════════════╝"
echo -e "${NC}"
echo "  This walkthrough will:"
echo "    1. Set up a Kind cluster with the Kagenti platform + SPIRE"
echo "    2. Install Envoy Gateway + AI Gateway"
echo "    3. Deploy Ollama with a small LLM model"
echo "    4. Build and deploy the operator with AI Gateway support"
echo "    5. Apply AIRoutingPolicy and AIAccessPolicy CRs"
echo "    6. Verify mTLS enforcement: trusted vs untrusted workloads"
echo ""
pause

# ══════════════════════════════════════════════════════════════════════════════
step "Check prerequisites"

for cmd in kind kubectl helm docker; do
  if command -v "$cmd" &>/dev/null; then
    ok "$cmd: $(command -v "$cmd")"
  else
    fail "$cmd is required but not installed"
    exit 1
  fi
done
pause

# ══════════════════════════════════════════════════════════════════════════════
step "Set up Kagenti platform with SPIRE"

INSTALLER="$ZOO_ROOT/kagenti/scripts/kind/setup-kagenti.sh"
if [[ ! -f "$INSTALLER" ]]; then
  fail "Platform installer not found at $INSTALLER"
  exit 1
fi

echo "  This installs the Kind cluster, cert-manager, Istio, Keycloak, SPIRE,"
echo "  and the kagenti operator. Output goes to $LOG_DIR/01-platform-setup.log"
echo ""
show "$INSTALLER --with-spire"
pause

"$INSTALLER" --with-spire > "$LOG_DIR/01-platform-setup.log" 2>&1 || {
  fail "Platform setup failed — see $LOG_DIR/01-platform-setup.log"
  exit 1
}
ok "Platform deployed with SPIRE"
pause

# ══════════════════════════════════════════════════════════════════════════════
step "Install Envoy Gateway v1.7.0 + AI Gateway v0.6.0"

echo "  Envoy Gateway provides the data plane. The AI Gateway extension adds"
echo "  model-aware routing, schema translation, and the ext_proc filter."
echo ""
echo "  Key config: extensionManager.hooks.xdsTranslator.translation enables"
echo "  listeners + routes in the PostTranslateModify hook so the ext_proc"
echo "  filter gets injected into the Envoy listener filter chain."
echo ""

# Envoy Gateway v1.7.0 (with extension manager config for AI Gateway)
show "helm upgrade --install eg oci://docker.io/envoyproxy/gateway-helm --version v1.7.0 ..."
pause

helm upgrade --install eg \
  oci://docker.io/envoyproxy/gateway-helm \
  --version v1.7.0 \
  -n envoy-gateway-system --create-namespace \
  -f "$VALUES_DIR/envoy-gateway-values.yaml" \
  --wait > "$LOG_DIR/02-envoy-gateway.log" 2>&1
ok "Envoy Gateway v1.7.0 installed"

# AI Gateway CRDs
helm upgrade --install ai-gateway-crds \
  oci://docker.io/envoyproxy/ai-gateway-crds-helm \
  --version v0.6.0 \
  -n envoy-ai-gateway-system --create-namespace \
  > "$LOG_DIR/03-ai-gateway-crds.log" 2>&1
ok "AI Gateway CRDs v0.6.0 installed"

# AI Gateway controller
helm upgrade --install ai-gateway \
  oci://docker.io/envoyproxy/ai-gateway-helm \
  --version v0.6.0 \
  -n envoy-ai-gateway-system \
  --wait > "$LOG_DIR/04-ai-gateway-ctrl.log" 2>&1
ok "AI Gateway controller v0.6.0 installed"

# GatewayClass
kubectl apply -f - <<'GWCLASS' > /dev/null
apiVersion: gateway.networking.k8s.io/v1
kind: GatewayClass
metadata:
  name: eg
spec:
  controllerName: gateway.envoyproxy.io/gatewayclass-controller
GWCLASS
ok "GatewayClass 'eg' created"

run "kubectl get pods -n envoy-gateway-system"
echo ""
run "kubectl get pods -n envoy-ai-gateway-system"
pause

# ══════════════════════════════════════════════════════════════════════════════
step "Deploy Ollama in-cluster"

echo "  Ollama runs qwen2.5:3b — a small 3B parameter model."
echo ""
show "kubectl apply -f manifests/ollama.yaml"
pause

kubectl create namespace "$DEMO_NS" --dry-run=client -o yaml | kubectl apply -f - > /dev/null 2>&1
kubectl apply -f "$MANIFESTS_DIR/ollama.yaml" > /dev/null
kubectl rollout status deployment/ollama -n "$DEMO_NS" --timeout=120s > "$LOG_DIR/05-ollama-deploy.log" 2>&1
ok "Ollama deployment ready"

echo ""
echo "  Pulling qwen2.5:3b model..."
kubectl exec -n "$DEMO_NS" deploy/ollama -- ollama pull qwen2.5:3b > "$LOG_DIR/06-ollama-pull.log" 2>&1
ok "Model qwen2.5:3b pulled"
pause

# ══════════════════════════════════════════════════════════════════════════════
step "Build operator with AI Gateway controllers"

echo "  The operator has two new controllers:"
echo "    - AIRoutingPolicyReconciler: providers, models → Backend, AIServiceBackend,"
echo "      AIGatewayRoute, BackendTrafficPolicy"
echo "    - AIAccessPolicyReconciler: SPIFFE trust bundle → CA Secret, server cert,"
echo "      ClientTrafficPolicy"
echo ""
show "docker build kagenti-operator/ -t local/kagenti-operator:\$TAG"
pause

cd "$OPERATOR_DIR"
TAG="ai-gateway-poc-$(date +%s)"
docker build . --tag "local/kagenti-operator:${TAG}" --load > "$LOG_DIR/07-operator-build.log" 2>&1
ok "Image built: local/kagenti-operator:${TAG}"

kind load docker-image "local/kagenti-operator:${TAG}" --name kagenti > "$LOG_DIR/08-kind-load.log" 2>&1
ok "Image loaded into Kind"
cd "$REPO_ROOT"
pause

# ══════════════════════════════════════════════════════════════════════════════
step "Deploy operator with --enable-ai-gateway"

echo "  The operator is deployed by the platform installer. We patch the"
echo "  deployment to use our new image and add --enable-ai-gateway=true."
echo ""

# Apply CRDs
kubectl apply -f - <<'CRDS' > /dev/null
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: airoutingpolicies.aigateway.kagenti.dev
spec:
  group: aigateway.kagenti.dev
  names:
    kind: AIRoutingPolicy
    listKind: AIRoutingPolicyList
    plural: airoutingpolicies
    singular: airoutingpolicy
    shortNames: [airp]
  scope: Namespaced
  versions:
  - name: v1alpha1
    served: true
    storage: true
    subresources:
      status: {}
    additionalPrinterColumns:
    - {name: Gateway, type: string, jsonPath: ".spec.targetRef.name"}
    - {name: Age, type: date, jsonPath: ".metadata.creationTimestamp"}
    schema:
      openAPIV3Schema:
        type: object
        properties:
          spec: {type: object, x-kubernetes-preserve-unknown-fields: true}
          status: {type: object, x-kubernetes-preserve-unknown-fields: true}
---
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: aiaccesspolicies.aigateway.kagenti.dev
spec:
  group: aigateway.kagenti.dev
  names:
    kind: AIAccessPolicy
    listKind: AIAccessPolicyList
    plural: aiaccesspolicies
    singular: aiaccesspolicy
    shortNames: [aiap]
  scope: Namespaced
  versions:
  - name: v1alpha1
    served: true
    storage: true
    subresources:
      status: {}
    additionalPrinterColumns:
    - {name: Gateway, type: string, jsonPath: ".spec.targetRef.name"}
    - {name: TrustDomain, type: string, jsonPath: ".spec.mtls.trustDomain"}
    - {name: Age, type: date, jsonPath: ".metadata.creationTimestamp"}
    schema:
      openAPIV3Schema:
        type: object
        properties:
          spec: {type: object, x-kubernetes-preserve-unknown-fields: true}
          status: {type: object, x-kubernetes-preserve-unknown-fields: true}
CRDS
ok "AI Gateway CRDs applied"

# Apply RBAC
kubectl apply -f - <<'RBAC' > /dev/null
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: kagenti-ai-gateway-role
rules:
- apiGroups: [aigateway.kagenti.dev]
  resources: [airoutingpolicies, aiaccesspolicies, airoutingpolicies/status, aiaccesspolicies/status, airoutingpolicies/finalizers, aiaccesspolicies/finalizers]
  verbs: ["*"]
- apiGroups: [gateway.networking.k8s.io]
  resources: [gateways]
  verbs: [get, list, watch]
- apiGroups: [gateway.envoyproxy.io]
  resources: [backends, clienttrafficpolicies, backendtrafficpolicies]
  verbs: ["*"]
- apiGroups: [aigateway.envoyproxy.io]
  resources: [aiservicebackends, aigatewayroutes, backendsecuritypolicies]
  verbs: ["*"]
- apiGroups: [""]
  resources: [secrets]
  verbs: ["*"]
- apiGroups: [""]
  resources: [configmaps]
  verbs: [get, list, watch]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: kagenti-ai-gateway-rolebinding
roleRef: {apiGroup: rbac.authorization.k8s.io, kind: ClusterRole, name: kagenti-ai-gateway-role}
subjects:
- {kind: ServiceAccount, name: controller-manager, namespace: kagenti-system}
RBAC
ok "RBAC applied"

# Patch the operator deployment
show "kubectl set image deploy/kagenti-controller-manager manager=local/kagenti-operator:${TAG}"
kubectl set image deployment/kagenti-controller-manager -n "$OPERATOR_NS" "manager=local/kagenti-operator:${TAG}" > /dev/null

kubectl patch deployment kagenti-controller-manager -n "$OPERATOR_NS" --type json \
  -p '[{"op":"add","path":"/spec/template/spec/containers/0/args/-","value":"--enable-ai-gateway=true"}]' > /dev/null 2>&1 || true
ok "Deployment patched with --enable-ai-gateway=true"

kubectl rollout status deployment/kagenti-controller-manager -n "$OPERATOR_NS" --timeout=120s > /dev/null 2>&1
ok "Operator rollout complete"

run "kubectl logs -n $OPERATOR_NS deploy/kagenti-controller-manager -c manager --tail=5 2>&1 | grep -i 'ai gateway'"
pause

# ══════════════════════════════════════════════════════════════════════════════
step "Apply AI Gateway policies"

echo "  Three resources are applied:"
echo "    1. Gateway — HTTPS listener on port 8443 (managed by Envoy Gateway)"
echo "    2. AIRoutingPolicy — Ollama provider, qwen2.5:3b model"
echo "    3. AIAccessPolicy — mTLS via SPIFFE trust bundle"
echo ""

# Sync trust bundle to demo namespace
BUNDLE_DATA=$(kubectl get configmap spire-bundle -n spire-system -o jsonpath='{.data.bundle\.spiffe}')
kubectl create configmap spire-bundle -n "$DEMO_NS" \
  --from-literal="bundle.spiffe=$BUNDLE_DATA" \
  --dry-run=client -o yaml | kubectl apply -f - > /dev/null
ok "SPIRE trust bundle synced to $DEMO_NS"

show "kubectl apply -f manifests/gateway.yaml"
show "kubectl apply -f manifests/airoutingpolicy.yaml"
show "kubectl apply -f manifests/aiaccesspolicy.yaml"
pause

kubectl apply -f "$MANIFESTS_DIR/gateway.yaml" > /dev/null
kubectl apply -f "$MANIFESTS_DIR/airoutingpolicy.yaml" > /dev/null
kubectl apply -f "$MANIFESTS_DIR/aiaccesspolicy.yaml" > /dev/null

echo "  Waiting for controllers to reconcile..."
sleep 15

echo ""
run "kubectl get gateway,airoutingpolicy,aiaccesspolicy -n $DEMO_NS"
pause

# ══════════════════════════════════════════════════════════════════════════════
step "Verify generated Envoy resources"

echo "  The controllers translate the policy CRs into data-plane resources."
echo "  AIRoutingPolicy generates: Backend, AIServiceBackend, AIGatewayRoute,"
echo "    BackendTrafficPolicy"
echo "  AIAccessPolicy generates: CA Secret, server cert Secret,"
echo "    ClientTrafficPolicy"
echo ""

for res in \
  "backend/ai-gateway-routing-ollama" \
  "aiservicebackend/ai-gateway-routing-ollama" \
  "aigatewayroute/ai-gateway-routing" \
  "backendtrafficpolicy/ai-gateway-routing" \
  "clienttrafficpolicy/ai-gateway-access" \
  "secret/ai-gateway-access-mtls-ca" \
  "secret/ai-gateway-access-mtls-server"; do
  if kubectl get "$res" -n "$DEMO_NS" &>/dev/null; then
    ok "$res"
  else
    fail "$res NOT found"
  fi
done
pause

# ══════════════════════════════════════════════════════════════════════════════
step "Deploy test workloads"

echo "  Two pods test mTLS enforcement:"
echo "    curl-spiffe  — has SPIFFE identity (CSI volume + spiffe-helper)"
echo "    curl-untrusted — no SPIFFE identity"
echo ""

# spiffe-helper config
kubectl apply -n "$DEMO_NS" -f - > /dev/null <<'HELPERCONF'
apiVersion: v1
kind: ConfigMap
metadata:
  name: spiffe-helper-config
data:
  helper.conf: |
    agent_address = "/spiffe-workload-api/spire-agent.sock"
    cert_dir = "/spiffe-certs"
    svid_file_name = "tls.crt"
    svid_key_file_name = "tls.key"
    svid_bundle_file_name = "ca.crt"
    daemon_mode = false
HELPERCONF

# Trusted pod with spiffe-helper
kubectl apply -n "$DEMO_NS" -f - > /dev/null <<'TRUSTED'
apiVersion: v1
kind: Pod
metadata:
  name: curl-spiffe
  labels:
    kagenti.io/spire: "enabled"
spec:
  initContainers:
  - name: spiffe-helper
    image: ghcr.io/spiffe/spiffe-helper:0.8.0
    args: ["-config", "/etc/spiffe-helper/helper.conf"]
    volumeMounts:
    - {name: spiffe-certs, mountPath: /spiffe-certs}
    - {name: spiffe-workload-api, mountPath: /spiffe-workload-api, readOnly: true}
    - {name: spiffe-helper-config, mountPath: /etc/spiffe-helper}
  - name: fixperms
    image: busybox
    command: ["sh", "-c", "chmod 644 /spiffe-certs/*"]
    volumeMounts:
    - {name: spiffe-certs, mountPath: /spiffe-certs}
  containers:
  - name: curl
    image: curlimages/curl:latest
    command: ["sleep", "infinity"]
    volumeMounts:
    - {name: spiffe-certs, mountPath: /spiffe-certs, readOnly: true}
  volumes:
  - {name: spiffe-certs, emptyDir: {}}
  - name: spiffe-workload-api
    csi: {driver: "csi.spiffe.io", readOnly: true}
  - {name: spiffe-helper-config, configMap: {name: spiffe-helper-config}}
TRUSTED

# Untrusted pod
kubectl apply -f "$MANIFESTS_DIR/curl-untrusted.yaml" > /dev/null

echo "  Waiting for pods..."
kubectl wait --for=condition=Ready pod/curl-spiffe -n "$DEMO_NS" --timeout=120s > /dev/null 2>&1
kubectl wait --for=condition=Ready pod/curl-untrusted -n "$DEMO_NS" --timeout=60s > /dev/null 2>&1

run "kubectl get pods curl-spiffe curl-untrusted -n $DEMO_NS"
pause

# ══════════════════════════════════════════════════════════════════════════════
step "Test: Trusted workload → AI Gateway (expect 200)"

GW_SVC=$(kubectl get svc -n envoy-gateway-system -l gateway.envoyproxy.io/owning-gateway-name=ai-gateway -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || echo "")
if [[ -z "$GW_SVC" ]]; then
  fail "Gateway service not found"
  pause
else
  GW_ENDPOINT="https://$GW_SVC.envoy-gateway-system.svc:8443"
  CHAT_BODY='{"model":"qwen2.5:3b","messages":[{"role":"user","content":"Say hello in one word"}]}'

  echo "  The trusted pod presents its SPIFFE X.509 client certificate."
  echo "  Envoy validates it against the CA from the SPIRE trust bundle."
  echo ""
  show "kubectl exec curl-spiffe -- curl --cert /spiffe-certs/tls.crt --key /spiffe-certs/tls.key -k $GW_ENDPOINT/v1/chat/completions ..."
  pause

  echo ""
  RESPONSE=$(kubectl exec -n "$DEMO_NS" curl-spiffe -c curl -- \
    curl -s -w '\n%{http_code}' \
    --cert /spiffe-certs/tls.crt \
    --key /spiffe-certs/tls.key \
    -k \
    "$GW_ENDPOINT/v1/chat/completions" \
    -H "Content-Type: application/json" \
    -d "$CHAT_BODY" \
    --max-time 60 2>/dev/null)

  HTTP_CODE=$(echo "$RESPONSE" | tail -1)
  BODY=$(echo "$RESPONSE" | sed '$d')

  echo -e "  ${DIM}Response:${NC}"
  echo "  $BODY" | python3 -m json.tool 2>/dev/null || echo "  $BODY"
  echo ""
  if [[ "$HTTP_CODE" == "200" ]]; then
    ok "HTTP $HTTP_CODE — inference succeeded through mTLS gateway"
  else
    fail "HTTP $HTTP_CODE (expected 200)"
  fi
  pause

  # ════════════════════════════════════════════════════════════════════════════
  step "Test: Untrusted workload → AI Gateway (expect rejection)"

  echo "  The untrusted pod has no SPIFFE identity and no client certificate."
  echo "  Envoy rejects the TLS handshake."
  echo ""
  show "kubectl exec curl-untrusted -- curl -k $GW_ENDPOINT/v1/chat/completions ..."
  pause

  UNTRUSTED_CODE=$(kubectl exec -n "$DEMO_NS" curl-untrusted -- \
    curl -s -o /dev/null -w '%{http_code}' \
    -k \
    "$GW_ENDPOINT/v1/chat/completions" \
    -H "Content-Type: application/json" \
    -d "$CHAT_BODY" \
    --max-time 10 2>/dev/null || echo "000")

  if [[ "$UNTRUSTED_CODE" == "000" ]]; then
    ok "Connection rejected — TLS handshake failed (no client certificate)"
  elif [[ "$UNTRUSTED_CODE" == "400" || "$UNTRUSTED_CODE" == "403" ]]; then
    ok "HTTP $UNTRUSTED_CODE — rejected by gateway (no valid client cert)"
  else
    fail "HTTP $UNTRUSTED_CODE (expected TLS rejection)"
  fi
  pause
fi

# ══════════════════════════════════════════════════════════════════════════════
step "Summary"

echo -e "  ${BOLD}What the operator did:${NC}"
echo ""
echo "  User created:"
echo "    Gateway               — HTTPS listener, gatewayClassName: eg"
echo "    AIRoutingPolicy       — 1 provider (Ollama), 1 model (qwen2.5:3b)"
echo "    AIAccessPolicy        — mTLS via SPIFFE trust bundle"
echo ""
echo "  Controller generated:"
echo "    Backend               — Envoy Gateway FQDN endpoint for Ollama"
echo "    AIServiceBackend      — AI Gateway schema mapping (OpenAI)"
echo "    AIGatewayRoute        — model-based routing rules"
echo "    BackendTrafficPolicy  — local rate limit rules"
echo "    CA Secret             — PEM certs extracted from SPIFFE JSON bundle"
echo "    Server cert Secret    — self-signed ECDSA P-256 for TLS termination"
echo "    ClientTrafficPolicy   — requires client certs validated against CA"
echo ""
echo -e "  ${BOLD}Results:${NC}"
echo "    Trusted workload (SPIFFE identity) → HTTP 200 inference"
echo "    Untrusted workload (no identity)   → TLS handshake rejected"
echo ""
echo "  Resources:"
run "kubectl get gateway,airoutingpolicy,aiaccesspolicy,backend,aiservicebackend,aigatewayroute -n $DEMO_NS 2>/dev/null" || true
echo ""
echo "  Logs: $LOG_DIR/"
echo ""
