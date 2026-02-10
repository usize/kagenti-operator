/*
Copyright 2025.

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

package signature

import (
	"context"
	"testing"
	"time"

	agentv1alpha1 "github.com/kagenti/operator/api/v1alpha1"
)

// --- NewProvider factory tests ---

func TestNewProvider_NilConfig(t *testing.T) {
	_, err := NewProvider(nil)
	if err == nil {
		t.Error("Expected error for nil config")
	}
}

func TestNewProvider_UnknownType(t *testing.T) {
	_, err := NewProvider(&Config{Type: "unknown"})
	if err == nil {
		t.Error("Expected error for unknown provider type")
	}
}

func TestNewProvider_None(t *testing.T) {
	p, err := NewProvider(&Config{Type: ProviderTypeNone})
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if p.Name() != "noop" {
		t.Errorf("Expected name 'noop', got '%s'", p.Name())
	}
}

func TestNewProvider_Secret_MissingName(t *testing.T) {
	_, err := NewProvider(&Config{
		Type:            ProviderTypeSecret,
		SecretNamespace: "default",
	})
	if err == nil {
		t.Error("Expected error when SecretName is empty")
	}
}

func TestNewProvider_Secret_MissingNamespace(t *testing.T) {
	_, err := NewProvider(&Config{
		Type:       ProviderTypeSecret,
		SecretName: "my-secret",
	})
	if err == nil {
		t.Error("Expected error when SecretNamespace is empty")
	}
}

func TestNewProvider_Secret_Valid(t *testing.T) {
	p, err := NewProvider(&Config{
		Type:            ProviderTypeSecret,
		SecretName:      "a2a-keys",
		SecretNamespace: "kagenti-system",
	})
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if p.Name() != "secret" {
		t.Errorf("Expected name 'secret', got '%s'", p.Name())
	}
}

func TestNewProvider_JWKS_MissingURL(t *testing.T) {
	_, err := NewProvider(&Config{Type: ProviderTypeJWKS})
	if err == nil {
		t.Error("Expected error when JWKSURL is empty")
	}
}

func TestNewProvider_JWKS_Valid(t *testing.T) {
	p, err := NewProvider(&Config{
		Type:    ProviderTypeJWKS,
		JWKSURL: "https://example.com/.well-known/jwks.json",
	})
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if p.Name() != "jwks" {
		t.Errorf("Expected name 'jwks', got '%s'", p.Name())
	}
}

func TestNewProvider_JWKS_CustomCacheTTL(t *testing.T) {
	p, err := NewProvider(&Config{
		Type:         ProviderTypeJWKS,
		JWKSURL:      "https://example.com/.well-known/jwks.json",
		JWKSCacheTTL: 10 * time.Minute,
	})
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	jwksProvider, ok := p.(*JWKSProvider)
	if !ok {
		t.Fatal("Expected *JWKSProvider type")
	}
	if jwksProvider.cacheTTL != 10*time.Minute {
		t.Errorf("Expected cacheTTL=10m, got %v", jwksProvider.cacheTTL)
	}
}

// --- NoOpProvider tests ---

func TestNoOpProvider_AlwaysVerified(t *testing.T) {
	p := NewNoOpProvider()
	ctx := context.Background()

	// With nil signatures
	result, err := p.VerifySignature(ctx, newCardData("Agent", "http://a:8000", "1.0"), nil)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if !result.Verified {
		t.Error("NoOpProvider should always return verified=true")
	}

	// With a JWS signature
	result, err = p.VerifySignature(ctx, newCardData("Agent", "http://a:8000", "1.0"),
		[]agentv1alpha1.AgentCardSignature{
			{Protected: "eyJhbGciOiJSUzI1NiJ9", Signature: "fake-sig"},
		})
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if !result.Verified {
		t.Error("NoOpProvider should always return verified=true, even with signatures")
	}
}

func TestNoOpProvider_Name(t *testing.T) {
	p := NewNoOpProvider()
	if p.Name() != "noop" {
		t.Errorf("Expected 'noop', got '%s'", p.Name())
	}
}

// --- Config validation tests ---

func TestConfig_ProviderTypes(t *testing.T) {
	tests := []struct {
		name     string
		pt       ProviderType
		expected string
	}{
		{"secret", ProviderTypeSecret, "secret"},
		{"jwks", ProviderTypeJWKS, "jwks"},
		{"none", ProviderTypeNone, "none"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if string(tt.pt) != tt.expected {
				t.Errorf("Expected %s, got %s", tt.expected, tt.pt)
			}
		})
	}
}
