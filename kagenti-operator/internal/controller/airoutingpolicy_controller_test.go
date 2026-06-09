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

package controller

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	aigatewayv1alpha1 "github.com/kagenti/operator/api/aigateway/v1alpha1"
)

func TestParseEndpoint(t *testing.T) {
	tests := []struct {
		name     string
		endpoint string
		wantHost string
		wantPort int32
		wantErr  bool
	}{
		{
			name:     "HTTP with explicit port",
			endpoint: "http://ollama.team1.svc:11434",
			wantHost: "ollama.team1.svc",
			wantPort: 11434,
		},
		{
			name:     "HTTPS with explicit port",
			endpoint: "https://api.openai.com:443",
			wantHost: "api.openai.com",
			wantPort: 443,
		},
		{
			name:     "HTTPS default port",
			endpoint: "https://api.openai.com/v1",
			wantHost: "api.openai.com",
			wantPort: 443,
		},
		{
			name:     "HTTP default port",
			endpoint: "http://ollama.svc/api",
			wantHost: "ollama.svc",
			wantPort: 80,
		},
		{
			name:     "Empty URL",
			endpoint: "",
			wantErr:  true,
		},
		{
			name:     "Invalid URL",
			endpoint: "://invalid",
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			host, port, err := parseEndpoint(tt.endpoint)
			if tt.wantErr {
				if err == nil {
					t.Error("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if host != tt.wantHost {
				t.Errorf("host: got %q, want %q", host, tt.wantHost)
			}
			if port != tt.wantPort {
				t.Errorf("port: got %d, want %d", port, tt.wantPort)
			}
		})
	}
}

func TestBuildRoutingIntent(t *testing.T) {
	r := &AIRoutingPolicyReconciler{}

	rpm := 100
	policy := &aigatewayv1alpha1.AIRoutingPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-policy",
			Namespace: "team1",
		},
		Spec: aigatewayv1alpha1.AIRoutingPolicySpec{
			TargetRef: aigatewayv1alpha1.TargetRef{
				Group: "gateway.networking.k8s.io",
				Kind:  "Gateway",
				Name:  "ai-gateway",
			},
			Providers: []aigatewayv1alpha1.ProviderSpec{
				{
					Name:     "ollama",
					Endpoint: "http://ollama.svc:11434",
					Schema:   "OpenAI",
				},
				{
					Name:     "openai",
					Endpoint: "https://api.openai.com/v1",
					Schema:   "OpenAI",
					Credentials: &aigatewayv1alpha1.CredentialSpec{
						Type: "APIKey",
						SecretRef: &aigatewayv1alpha1.SecretKeyRef{
							Name: "openai-secret",
							Key:  "api-key",
						},
					},
				},
			},
			Models: []aigatewayv1alpha1.ModelSpec{
				{
					Name: "qwen2.5:3b",
					Backends: []aigatewayv1alpha1.ModelBackend{
						{Provider: "ollama", Model: "qwen2.5:3b"},
					},
					RateLimit: &aigatewayv1alpha1.RateLimitSpec{
						RequestsPerMinute: &rpm,
					},
				},
			},
		},
	}

	intent, err := r.buildRoutingIntent(policy)
	if err != nil {
		t.Fatalf("buildRoutingIntent failed: %v", err)
	}

	if intent.PolicyName != "test-policy" {
		t.Errorf("expected PolicyName %q, got %q", "test-policy", intent.PolicyName)
	}
	if intent.GatewayName != "ai-gateway" {
		t.Errorf("expected GatewayName %q, got %q", "ai-gateway", intent.GatewayName)
	}
	if len(intent.Providers) != 2 {
		t.Fatalf("expected 2 providers, got %d", len(intent.Providers))
	}
	if intent.Providers[0].Host != "ollama.svc" {
		t.Errorf("expected host %q, got %q", "ollama.svc", intent.Providers[0].Host)
	}
	if intent.Providers[0].Port != 11434 {
		t.Errorf("expected port 11434, got %d", intent.Providers[0].Port)
	}
	if intent.Providers[1].Credentials == nil {
		t.Fatal("expected credentials for openai provider")
	}
	if intent.Providers[1].Credentials.SecretName != "openai-secret" {
		t.Errorf("expected secret name %q, got %q", "openai-secret", intent.Providers[1].Credentials.SecretName)
	}
	if intent.Providers[1].Port != 443 {
		t.Errorf("expected HTTPS default port 443, got %d", intent.Providers[1].Port)
	}
	if len(intent.Models) != 1 {
		t.Fatalf("expected 1 model, got %d", len(intent.Models))
	}
	if intent.Models[0].RateLimit == nil || intent.Models[0].RateLimit.RequestsPerMinute == nil {
		t.Fatal("expected rate limit on model")
	}
	if *intent.Models[0].RateLimit.RequestsPerMinute != 100 {
		t.Errorf("expected 100 rpm, got %d", *intent.Models[0].RateLimit.RequestsPerMinute)
	}
}
