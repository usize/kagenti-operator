/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package envoy

import (
	"testing"

	corev1 "k8s.io/api/core/v1"

	aigwv1a1 "github.com/envoyproxy/ai-gateway/api/v1alpha1"
	egv1a1 "github.com/envoyproxy/gateway/api/v1alpha1"

	"github.com/kagenti/operator/internal/aigateway"
)

func intPtr(v int) *int { return &v }

func basicRoutingIntent() *aigateway.RoutingIntent {
	return &aigateway.RoutingIntent{
		PolicyName:      "test-routing",
		PolicyNamespace: "team1",
		GatewayName:     "ai-gateway",
		Providers: []aigateway.ProviderIntent{
			{
				Name:   "ollama",
				Host:   "ollama.team1.svc",
				Port:   11434,
				Schema: "OpenAI",
			},
		},
		Models: []aigateway.ModelIntent{
			{
				Name: "qwen2.5:3b",
				Backends: []aigateway.ModelBackendIntent{
					{ProviderName: "ollama", ModelName: "qwen2.5:3b"},
				},
			},
		},
	}
}

func TestRenderRouting_BasicProvider(t *testing.T) {
	intent := basicRoutingIntent()
	objects := RenderRouting(intent)

	// Expect: 1 Backend + 1 AIServiceBackend + 1 AIGatewayRoute = 3
	if len(objects) != 3 {
		t.Fatalf("expected 3 objects, got %d", len(objects))
	}

	// Backend
	backend, ok := objects[0].(*egv1a1.Backend)
	if !ok {
		t.Fatal("first object is not a Backend")
	}
	if backend.Name != "test-routing-ollama" {
		t.Errorf("expected backend name %q, got %q", "test-routing-ollama", backend.Name)
	}
	if backend.Namespace != "team1" {
		t.Errorf("expected namespace %q, got %q", "team1", backend.Namespace)
	}
	if len(backend.Spec.Endpoints) != 1 {
		t.Fatalf("expected 1 endpoint, got %d", len(backend.Spec.Endpoints))
	}
	if backend.Spec.Endpoints[0].FQDN == nil {
		t.Fatal("expected FQDN endpoint")
	}
	if backend.Spec.Endpoints[0].FQDN.Hostname != "ollama.team1.svc" {
		t.Errorf("expected hostname %q, got %q", "ollama.team1.svc", backend.Spec.Endpoints[0].FQDN.Hostname)
	}
	if backend.Spec.Endpoints[0].FQDN.Port != 11434 {
		t.Errorf("expected port 11434, got %d", backend.Spec.Endpoints[0].FQDN.Port)
	}

	// AIServiceBackend
	aisb, ok := objects[1].(*aigwv1a1.AIServiceBackend)
	if !ok {
		t.Fatal("second object is not an AIServiceBackend")
	}
	if aisb.Name != "test-routing-ollama" {
		t.Errorf("expected AIServiceBackend name %q, got %q", "test-routing-ollama", aisb.Name)
	}
	if aisb.Spec.APISchema.Name != "OpenAI" {
		t.Errorf("expected schema OpenAI, got %q", aisb.Spec.APISchema.Name)
	}
	if aisb.Spec.BackendRef.Group == nil || string(*aisb.Spec.BackendRef.Group) != "gateway.envoyproxy.io" {
		t.Error("expected BackendRef to point to gateway.envoyproxy.io Backend")
	}

	// AIGatewayRoute
	route, ok := objects[2].(*aigwv1a1.AIGatewayRoute)
	if !ok {
		t.Fatal("third object is not an AIGatewayRoute")
	}
	if route.Name != "test-routing" {
		t.Errorf("expected route name %q, got %q", "test-routing", route.Name)
	}
	if len(route.Spec.Rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(route.Spec.Rules))
	}
	if len(route.Spec.Rules[0].BackendRefs) != 1 {
		t.Fatalf("expected 1 backendRef, got %d", len(route.Spec.Rules[0].BackendRefs))
	}
	// BackendRefs should NOT have group/kind set (defaults to AIServiceBackend)
	ref := route.Spec.Rules[0].BackendRefs[0]
	if ref.Group != nil {
		t.Error("AIGatewayRoute backendRef should not have group set")
	}
	if ref.Kind != nil {
		t.Error("AIGatewayRoute backendRef should not have kind set")
	}
	if ref.ModelNameOverride != "qwen2.5:3b" {
		t.Errorf("expected ModelNameOverride %q, got %q", "qwen2.5:3b", ref.ModelNameOverride)
	}
}

func TestRenderRouting_ProviderWithCredentials(t *testing.T) {
	intent := &aigateway.RoutingIntent{
		PolicyName:      "test-routing",
		PolicyNamespace: "team1",
		GatewayName:     "ai-gateway",
		Providers: []aigateway.ProviderIntent{
			{
				Name:   "openai",
				Host:   "api.openai.com",
				Port:   443,
				Schema: "OpenAI",
				Credentials: &aigateway.CredentialIntent{
					Type:       "APIKey",
					SecretName: "openai-secret",
					SecretKey:  "api-key",
				},
			},
		},
		Models: []aigateway.ModelIntent{
			{
				Name: "gpt-4o",
				Backends: []aigateway.ModelBackendIntent{
					{ProviderName: "openai", ModelName: "gpt-4o"},
				},
			},
		},
	}

	objects := RenderRouting(intent)

	// Expect: 1 Backend + 1 AIServiceBackend + 1 BSP + 1 Route = 4
	if len(objects) != 4 {
		t.Fatalf("expected 4 objects, got %d", len(objects))
	}

	bsp, ok := objects[2].(*aigwv1a1.BackendSecurityPolicy)
	if !ok {
		t.Fatal("third object is not a BackendSecurityPolicy")
	}
	if bsp.Spec.Type != aigwv1a1.BackendSecurityPolicyTypeAPIKey {
		t.Errorf("expected APIKey type, got %q", bsp.Spec.Type)
	}
	if bsp.Spec.APIKey == nil || bsp.Spec.APIKey.SecretRef == nil {
		t.Fatal("expected APIKey secretRef")
	}
	if string(bsp.Spec.APIKey.SecretRef.Name) != "openai-secret" {
		t.Errorf("expected secret name %q, got %q", "openai-secret", bsp.Spec.APIKey.SecretRef.Name)
	}
}

func TestRenderRouting_WithRateLimit(t *testing.T) {
	intent := basicRoutingIntent()
	rpm := 10
	intent.Models[0].RateLimit = &aigateway.RateLimitIntent{
		RequestsPerMinute: &rpm,
	}

	objects := RenderRouting(intent)

	// Expect: 1 Backend + 1 AIServiceBackend + 1 Route + 1 BTP = 4
	if len(objects) != 4 {
		t.Fatalf("expected 4 objects, got %d", len(objects))
	}

	// Check route has LLMRequestCosts
	route, ok := objects[2].(*aigwv1a1.AIGatewayRoute)
	if !ok {
		t.Fatal("third object is not an AIGatewayRoute")
	}
	if len(route.Spec.LLMRequestCosts) != 3 {
		t.Errorf("expected 3 LLMRequestCosts, got %d", len(route.Spec.LLMRequestCosts))
	}

	// Check BTP
	btp, ok := objects[3].(*egv1a1.BackendTrafficPolicy)
	if !ok {
		t.Fatal("fourth object is not a BackendTrafficPolicy")
	}
	if btp.Spec.RateLimit == nil {
		t.Fatal("expected RateLimit to be set")
	}
	if btp.Spec.RateLimit.Local == nil {
		t.Fatal("expected Local rate limit")
	}
	if len(btp.Spec.RateLimit.Local.Rules) != 1 {
		t.Fatalf("expected 1 rate limit rule, got %d", len(btp.Spec.RateLimit.Local.Rules))
	}
	rule := btp.Spec.RateLimit.Local.Rules[0]
	if rule.Limit.Requests != 10 {
		t.Errorf("expected 10 requests, got %d", rule.Limit.Requests)
	}
	if rule.Limit.Unit != "Minute" {
		t.Errorf("expected unit Minute, got %q", rule.Limit.Unit)
	}
}

func TestRenderRouting_MultipleProviders(t *testing.T) {
	intent := &aigateway.RoutingIntent{
		PolicyName:      "multi",
		PolicyNamespace: "team1",
		GatewayName:     "ai-gateway",
		Providers: []aigateway.ProviderIntent{
			{Name: "ollama", Host: "ollama.svc", Port: 11434, Schema: "OpenAI"},
			{Name: "openai", Host: "api.openai.com", Port: 443, Schema: "OpenAI",
				Credentials: &aigateway.CredentialIntent{Type: "APIKey", SecretName: "openai-secret"}},
		},
		Models: []aigateway.ModelIntent{
			{
				Name: "gpt-4o",
				Backends: []aigateway.ModelBackendIntent{
					{ProviderName: "openai", ModelName: "gpt-4o", Priority: 0},
					{ProviderName: "ollama", ModelName: "llama3", Priority: 1},
				},
			},
		},
	}

	objects := RenderRouting(intent)

	// 2 Backends + 2 AIServiceBackends + 1 BSP + 1 Route = 6
	if len(objects) != 6 {
		t.Fatalf("expected 6 objects, got %d", len(objects))
	}

	// Verify the route has correct backend refs with priority
	route := objects[5].(*aigwv1a1.AIGatewayRoute)
	if len(route.Spec.Rules[0].BackendRefs) != 2 {
		t.Fatalf("expected 2 backendRefs, got %d", len(route.Spec.Rules[0].BackendRefs))
	}
	// Second backend should have priority=1
	if route.Spec.Rules[0].BackendRefs[1].Priority == nil {
		t.Fatal("expected priority on second backend")
	}
	if *route.Spec.Rules[0].BackendRefs[1].Priority != 1 {
		t.Errorf("expected priority 1, got %d", *route.Spec.Rules[0].BackendRefs[1].Priority)
	}
}

func TestRenderAccess_SelfSignedCert(t *testing.T) {
	intent := &aigateway.AccessIntent{
		PolicyName:      "test-access",
		PolicyNamespace: "team1",
		GatewayName:     "ai-gateway",
		TrustDomain:     "localtest.me",
		CACertPEM:       []byte("-----BEGIN CERTIFICATE-----\ntest\n-----END CERTIFICATE-----\n"),
	}

	objects, err := RenderAccess(intent)
	if err != nil {
		t.Fatalf("RenderAccess failed: %v", err)
	}

	// Expect: CA Secret + Server cert Secret + ClientTrafficPolicy = 3
	if len(objects) != 3 {
		t.Fatalf("expected 3 objects, got %d", len(objects))
	}

	// CA Secret
	caSecret, ok := objects[0].(*corev1.Secret)
	if !ok {
		t.Fatal("first object is not a Secret")
	}
	if caSecret.Name != "test-access-mtls-ca" {
		t.Errorf("expected CA secret name %q, got %q", "test-access-mtls-ca", caSecret.Name)
	}
	if _, ok := caSecret.Data["ca.crt"]; !ok {
		t.Error("CA secret missing ca.crt key")
	}

	// Server cert Secret
	serverSecret, ok := objects[1].(*corev1.Secret)
	if !ok {
		t.Fatal("second object is not a Secret")
	}
	if serverSecret.Name != "test-access-mtls-server" {
		t.Errorf("expected server cert name %q, got %q", "test-access-mtls-server", serverSecret.Name)
	}
	if serverSecret.Type != corev1.SecretTypeTLS {
		t.Errorf("expected TLS secret type, got %q", serverSecret.Type)
	}
	if _, ok := serverSecret.Data["tls.crt"]; !ok {
		t.Error("server secret missing tls.crt")
	}
	if _, ok := serverSecret.Data["tls.key"]; !ok {
		t.Error("server secret missing tls.key")
	}

	// ClientTrafficPolicy
	ctp, ok := objects[2].(*egv1a1.ClientTrafficPolicy)
	if !ok {
		t.Fatal("third object is not a ClientTrafficPolicy")
	}
	if ctp.Spec.TLS == nil || ctp.Spec.TLS.ClientValidation == nil {
		t.Fatal("expected TLS client validation")
	}
	if len(ctp.Spec.TLS.ClientValidation.CACertificateRefs) != 1 {
		t.Fatal("expected 1 CA certificate ref")
	}
	if string(ctp.Spec.TLS.ClientValidation.CACertificateRefs[0].Name) != "test-access-mtls-ca" {
		t.Error("CA cert ref points to wrong secret")
	}
}

func TestRenderAccess_WithServerCertRef(t *testing.T) {
	intent := &aigateway.AccessIntent{
		PolicyName:      "test-access",
		PolicyNamespace: "team1",
		GatewayName:     "ai-gateway",
		TrustDomain:     "localtest.me",
		CACertPEM:       []byte("test-ca"),
		ServerCertRef:   "my-existing-cert",
	}

	objects, err := RenderAccess(intent)
	if err != nil {
		t.Fatalf("RenderAccess failed: %v", err)
	}

	// With serverCertRef: CA Secret + ClientTrafficPolicy = 2 (no server cert generated)
	if len(objects) != 2 {
		t.Fatalf("expected 2 objects, got %d", len(objects))
	}

	if _, ok := objects[0].(*corev1.Secret); !ok {
		t.Fatal("first object is not a Secret")
	}
	if _, ok := objects[1].(*egv1a1.ClientTrafficPolicy); !ok {
		t.Fatal("second object is not a ClientTrafficPolicy")
	}
}

func TestRenderRouting_NoRateLimit_NoBTP(t *testing.T) {
	intent := basicRoutingIntent()
	objects := RenderRouting(intent)

	for _, obj := range objects {
		if _, ok := obj.(*egv1a1.BackendTrafficPolicy); ok {
			t.Error("did not expect BackendTrafficPolicy when no rate limits are set")
		}
	}
}

func TestResourceNamePrefix(t *testing.T) {
	name := ResourceNamePrefix("my-policy", "ollama")
	if name != "my-policy-ollama" {
		t.Errorf("expected %q, got %q", "my-policy-ollama", name)
	}
}
