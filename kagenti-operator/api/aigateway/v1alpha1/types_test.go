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

package v1alpha1

import (
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestGroupVersionConstants(t *testing.T) {
	if GroupVersion.Group != "aigateway.kagenti.dev" {
		t.Errorf("expected group %q, got %q", "aigateway.kagenti.dev", GroupVersion.Group)
	}
	if GroupVersion.Version != "v1alpha1" {
		t.Errorf("expected version %q, got %q", "v1alpha1", GroupVersion.Version)
	}
}

func TestSchemeRegistration(t *testing.T) {
	s := runtime.NewScheme()
	if err := AddToScheme(s); err != nil {
		t.Fatalf("AddToScheme failed: %v", err)
	}

	tests := []struct {
		name string
		gvk  schema.GroupVersionKind
	}{
		{
			name: "AIRoutingPolicy",
			gvk:  schema.GroupVersionKind{Group: "aigateway.kagenti.dev", Version: "v1alpha1", Kind: "AIRoutingPolicy"},
		},
		{
			name: "AIRoutingPolicyList",
			gvk:  schema.GroupVersionKind{Group: "aigateway.kagenti.dev", Version: "v1alpha1", Kind: "AIRoutingPolicyList"},
		},
		{
			name: "AIAccessPolicy",
			gvk:  schema.GroupVersionKind{Group: "aigateway.kagenti.dev", Version: "v1alpha1", Kind: "AIAccessPolicy"},
		},
		{
			name: "AIAccessPolicyList",
			gvk:  schema.GroupVersionKind{Group: "aigateway.kagenti.dev", Version: "v1alpha1", Kind: "AIAccessPolicyList"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !s.Recognizes(tt.gvk) {
				t.Errorf("scheme does not recognize %v", tt.gvk)
			}
		})
	}
}

func TestAIRoutingPolicyDeepCopy(t *testing.T) {
	rpm := 100
	original := &AIRoutingPolicy{
		Spec: AIRoutingPolicySpec{
			TargetRef: TargetRef{
				Group: "gateway.networking.k8s.io",
				Kind:  "Gateway",
				Name:  "test-gw",
			},
			Providers: []ProviderSpec{
				{
					Name:     "ollama",
					Endpoint: "http://ollama:11434",
					Schema:   "OpenAI",
				},
				{
					Name:     "openai",
					Endpoint: "https://api.openai.com/v1",
					Schema:   "OpenAI",
					Credentials: &CredentialSpec{
						Type: "APIKey",
						SecretRef: &SecretKeyRef{
							Name: "openai-secret",
							Key:  "api-key",
						},
					},
				},
			},
			Models: []ModelSpec{
				{
					Name: "test-model",
					Backends: []ModelBackend{
						{Provider: "ollama", Model: "qwen2.5:3b"},
					},
					RateLimit: &RateLimitSpec{
						RequestsPerMinute: &rpm,
					},
				},
			},
		},
	}

	copied := original.DeepCopy()

	// Verify independence
	copied.Spec.TargetRef.Name = "changed"
	if original.Spec.TargetRef.Name == "changed" {
		t.Error("DeepCopy shares TargetRef")
	}

	copied.Spec.Providers[0].Name = "changed"
	if original.Spec.Providers[0].Name == "changed" {
		t.Error("DeepCopy shares Providers slice")
	}

	copied.Spec.Providers[1].Credentials.SecretRef.Name = "changed"
	if original.Spec.Providers[1].Credentials.SecretRef.Name == "changed" {
		t.Error("DeepCopy shares Credentials pointer")
	}

	*copied.Spec.Models[0].RateLimit.RequestsPerMinute = 999
	if *original.Spec.Models[0].RateLimit.RequestsPerMinute == 999 {
		t.Error("DeepCopy shares RateLimit pointer")
	}
}

func TestAIAccessPolicyDeepCopy(t *testing.T) {
	original := &AIAccessPolicy{
		Spec: AIAccessPolicySpec{
			TargetRef: TargetRef{
				Group: "gateway.networking.k8s.io",
				Kind:  "Gateway",
				Name:  "test-gw",
			},
			MTLS: MTLSSpec{
				TrustDomain: "localtest.me",
				TrustBundleConfigMap: ConfigMapRef{
					Name:      "spire-bundle",
					Namespace: "spire-system",
					Key:       "bundle.spiffe",
				},
				ServerCertRef: &SecretRef{
					Name: "my-cert",
				},
			},
		},
	}

	copied := original.DeepCopy()

	copied.Spec.MTLS.ServerCertRef.Name = "changed"
	if original.Spec.MTLS.ServerCertRef.Name == "changed" {
		t.Error("DeepCopy shares ServerCertRef pointer")
	}

	copied.Spec.MTLS.TrustDomain = "changed"
	if original.Spec.MTLS.TrustDomain == "changed" {
		t.Error("DeepCopy shares MTLSSpec value")
	}
}

func TestDeepCopyObject(t *testing.T) {
	rp := &AIRoutingPolicy{}
	if obj := rp.DeepCopyObject(); obj == nil {
		t.Error("AIRoutingPolicy.DeepCopyObject returned nil")
	}

	ap := &AIAccessPolicy{}
	if obj := ap.DeepCopyObject(); obj == nil {
		t.Error("AIAccessPolicy.DeepCopyObject returned nil")
	}

	rpl := &AIRoutingPolicyList{}
	if obj := rpl.DeepCopyObject(); obj == nil {
		t.Error("AIRoutingPolicyList.DeepCopyObject returned nil")
	}

	apl := &AIAccessPolicyList{}
	if obj := apl.DeepCopyObject(); obj == nil {
		t.Error("AIAccessPolicyList.DeepCopyObject returned nil")
	}
}
