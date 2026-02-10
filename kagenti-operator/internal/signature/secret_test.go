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
	"crypto/rsa"
	"testing"

	agentv1alpha1 "github.com/kagenti/operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// --- Test helpers ---

// newSecretProviderForTest creates a SecretProvider with a fake K8s client
// that has the given secret pre-loaded.
func newSecretProviderForTest(t *testing.T, secret *corev1.Secret, config *Config) *SecretProvider {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("Failed to add corev1 to scheme: %v", err)
	}

	objs := []runtime.Object{}
	if secret != nil {
		objs = append(objs, secret)
	}
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(objs...).Build()

	p, err := NewSecretProvider(config)
	if err != nil {
		t.Fatalf("NewSecretProvider failed: %v", err)
	}
	sp := p.(*SecretProvider)
	sp.SetClient(fakeClient)
	return sp
}

// rsaPubKeyPEM delegates to generateRSAKeyPair in verifier_test.go (same package).
func rsaPubKeyPEM(t *testing.T) (*rsa.PrivateKey, []byte) {
	t.Helper()
	return generateRSAKeyPair(t)
}

// --- SecretProvider.VerifySignature tests (JWS format) ---

func TestSecretProvider_ValidJWSSignature(t *testing.T) {
	privKey, pubPEM := rsaPubKeyPEM(t)
	cardData := newCardData("Agent", "http://agent:8000", "1.0.0")
	jwsSig := buildJWSSignature(t, cardData, privKey, "my-key", "")

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "keys", Namespace: "system"},
		Data:       map[string][]byte{"my-key": pubPEM},
	}
	sp := newSecretProviderForTest(t, secret, &Config{
		Type:            ProviderTypeSecret,
		SecretName:      "keys",
		SecretNamespace: "system",
	})

	result, err := sp.VerifySignature(context.Background(), cardData,
		[]agentv1alpha1.AgentCardSignature{jwsSig})

	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if !result.Verified {
		t.Errorf("Expected verified=true. Details: %s", result.Details)
	}
}

func TestSecretProvider_EmptySignatures_RejectMode(t *testing.T) {
	_, pubPEM := rsaPubKeyPEM(t)
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "keys", Namespace: "system"},
		Data:       map[string][]byte{"key": pubPEM},
	}
	sp := newSecretProviderForTest(t, secret, &Config{
		Type:            ProviderTypeSecret,
		SecretName:      "keys",
		SecretNamespace: "system",
		AuditMode:       false,
	})

	result, _ := sp.VerifySignature(context.Background(),
		newCardData("A", "http://a:8000", "1.0"),
		nil) // no signatures
	if result.Verified {
		t.Error("Expected verified=false for empty signatures in reject mode")
	}
}

func TestSecretProvider_EmptySignatures_AuditMode(t *testing.T) {
	_, pubPEM := rsaPubKeyPEM(t)
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "keys", Namespace: "system"},
		Data:       map[string][]byte{"key": pubPEM},
	}
	sp := newSecretProviderForTest(t, secret, &Config{
		Type:            ProviderTypeSecret,
		SecretName:      "keys",
		SecretNamespace: "system",
		AuditMode:       true,
	})

	result, err := sp.VerifySignature(context.Background(),
		newCardData("A", "http://a:8000", "1.0"),
		nil) // no signatures
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if !result.Verified {
		t.Error("Expected verified=true for empty signatures in audit mode")
	}
	if indexOf(result.Details, "audit mode") < 0 {
		t.Errorf("Expected details to mention audit mode, got: %s", result.Details)
	}
}

func TestSecretProvider_WrongKey(t *testing.T) {
	privKey, _ := rsaPubKeyPEM(t)
	_, wrongPubPEM := rsaPubKeyPEM(t) // different key

	cardData := newCardData("Agent", "http://agent:8000", "1.0.0")
	jwsSig := buildJWSSignature(t, cardData, privKey, "my-key", "")

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "keys", Namespace: "system"},
		Data:       map[string][]byte{"my-key": wrongPubPEM},
	}
	sp := newSecretProviderForTest(t, secret, &Config{
		Type:            ProviderTypeSecret,
		SecretName:      "keys",
		SecretNamespace: "system",
	})

	result, _ := sp.VerifySignature(context.Background(), cardData,
		[]agentv1alpha1.AgentCardSignature{jwsSig})

	if result.Verified {
		t.Error("Expected verified=false when signing key doesn't match verification key")
	}
}

func TestSecretProvider_FallbackKeyRotation(t *testing.T) {
	// Card signed with old key. Secret has "my-key" updated to new key,
	// but old key still present under "old-key". Fallback should find it.
	privKey, pubPEM := rsaPubKeyPEM(t)
	_, newPubPEM := rsaPubKeyPEM(t)

	cardData := newCardData("Agent", "http://agent:8000", "1.0.0")
	jwsSig := buildJWSSignature(t, cardData, privKey, "my-key", "")

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "keys", Namespace: "system"},
		Data: map[string][]byte{
			"my-key":  newPubPEM, // primary — doesn't match
			"old-key": pubPEM,   // fallback — matches
		},
	}
	sp := newSecretProviderForTest(t, secret, &Config{
		Type:            ProviderTypeSecret,
		SecretName:      "keys",
		SecretNamespace: "system",
	})

	result, err := sp.VerifySignature(context.Background(), cardData,
		[]agentv1alpha1.AgentCardSignature{jwsSig})

	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if !result.Verified {
		t.Error("Expected verified=true via fallback key rotation")
	}
	if result.KeyID != "old-key" {
		t.Errorf("Expected keyID='old-key' (the key that matched), got '%s'", result.KeyID)
	}
}

func TestSecretProvider_SecretNotFound(t *testing.T) {
	sp := newSecretProviderForTest(t, nil, &Config{
		Type:            ProviderTypeSecret,
		SecretName:      "nonexistent",
		SecretNamespace: "system",
	})

	header := &ProtectedHeader{Algorithm: "RS256", KeyID: "key-1"}
	protB64, _ := EncodeProtectedHeader(header)

	result, err := sp.VerifySignature(context.Background(),
		newCardData("A", "http://a:8000", "1.0"),
		[]agentv1alpha1.AgentCardSignature{{Protected: protB64, Signature: "fake"}})

	if err == nil {
		t.Error("Expected error when secret is not found")
	}
	if result.Verified {
		t.Error("Expected verified=false when secret is not found")
	}
}

func TestSecretProvider_SecretNotFound_AuditMode(t *testing.T) {
	sp := newSecretProviderForTest(t, nil, &Config{
		Type:            ProviderTypeSecret,
		SecretName:      "nonexistent",
		SecretNamespace: "system",
		AuditMode:       true,
	})

	header := &ProtectedHeader{Algorithm: "RS256", KeyID: "key-1"}
	protB64, _ := EncodeProtectedHeader(header)

	result, err := sp.VerifySignature(context.Background(),
		newCardData("A", "http://a:8000", "1.0"),
		[]agentv1alpha1.AgentCardSignature{{Protected: protB64, Signature: "fake"}})

	if err != nil {
		t.Fatalf("Unexpected error in audit mode: %v", err)
	}
	if !result.Verified {
		t.Error("Expected verified=true in audit mode even when secret is missing")
	}
}

func TestSecretProvider_ClientNotSet(t *testing.T) {
	p, err := NewSecretProvider(&Config{
		Type:            ProviderTypeSecret,
		SecretName:      "keys",
		SecretNamespace: "system",
	})
	if err != nil {
		t.Fatalf("NewSecretProvider failed: %v", err)
	}
	// Don't call SetClient — client is nil

	header := &ProtectedHeader{Algorithm: "RS256", KeyID: "key-1"}
	protB64, _ := EncodeProtectedHeader(header)

	result, err := p.VerifySignature(context.Background(),
		newCardData("A", "http://a:8000", "1.0"),
		[]agentv1alpha1.AgentCardSignature{{Protected: protB64, Signature: "fake"}})

	if err == nil {
		t.Error("Expected error when client is not set")
	}
	if result.Verified {
		t.Error("Expected verified=false when client is not set")
	}
}

func TestSecretProvider_Name(t *testing.T) {
	p, _ := NewSecretProvider(&Config{
		Type:            ProviderTypeSecret,
		SecretName:      "keys",
		SecretNamespace: "system",
	})
	if p.Name() != "secret" {
		t.Errorf("Expected 'secret', got '%s'", p.Name())
	}
}

func TestSecretProvider_SpiffeIDExtracted(t *testing.T) {
	privKey, pubPEM := rsaPubKeyPEM(t)
	cardData := newCardData("Agent", "http://agent:8000", "1.0.0")
	spiffeID := "spiffe://cluster.local/ns/default/sa/agent-sa"
	jwsSig := buildJWSSignature(t, cardData, privKey, "my-key", spiffeID)

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "keys", Namespace: "system"},
		Data:       map[string][]byte{"my-key": pubPEM},
	}
	sp := newSecretProviderForTest(t, secret, &Config{
		Type:            ProviderTypeSecret,
		SecretName:      "keys",
		SecretNamespace: "system",
	})

	result, err := sp.VerifySignature(context.Background(), cardData,
		[]agentv1alpha1.AgentCardSignature{jwsSig})

	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if !result.Verified {
		t.Errorf("Expected verified=true. Details: %s", result.Details)
	}
	if result.SpiffeID != spiffeID {
		t.Errorf("Expected SpiffeID=%s, got %s", spiffeID, result.SpiffeID)
	}
}

// --- getKeyFromSecret tests ---

func TestGetKeyFromSecret_BySecretKey(t *testing.T) {
	sp := &SecretProvider{secretKey: "my-custom-key"}
	secret := &corev1.Secret{
		Data: map[string][]byte{
			"my-custom-key": []byte("key-data"),
			"other-key":     []byte("other-data"),
		},
	}

	data, err := sp.getKeyFromSecret(secret, "ignored-keyID")
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if string(data) != "key-data" {
		t.Errorf("Expected 'key-data', got '%s'", data)
	}
}

func TestGetKeyFromSecret_ByKeyID(t *testing.T) {
	sp := &SecretProvider{}
	secret := &corev1.Secret{
		Data: map[string][]byte{
			"signing-key": []byte("key-data-1"),
			"another-key": []byte("key-data-2"),
		},
	}

	data, err := sp.getKeyFromSecret(secret, "signing-key")
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if string(data) != "key-data-1" {
		t.Errorf("Expected 'key-data-1', got '%s'", data)
	}
}

func TestGetKeyFromSecret_ByKeyID_WithExtension(t *testing.T) {
	sp := &SecretProvider{}
	secret := &corev1.Secret{
		Data: map[string][]byte{
			"my-key.pem": []byte("key-data"),
		},
	}

	data, err := sp.getKeyFromSecret(secret, "my-key")
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if string(data) != "key-data" {
		t.Errorf("Expected 'key-data', got '%s'", data)
	}
}

func TestGetKeyFromSecret_CommonKeyNames(t *testing.T) {
	sp := &SecretProvider{}
	secret := &corev1.Secret{
		Data: map[string][]byte{
			"public.pem": []byte("key-data"),
		},
	}

	data, err := sp.getKeyFromSecret(secret, "")
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if string(data) != "key-data" {
		t.Errorf("Expected 'key-data', got '%s'", data)
	}
}

func TestGetKeyFromSecret_SingleKey(t *testing.T) {
	sp := &SecretProvider{}
	secret := &corev1.Secret{
		Data: map[string][]byte{
			"whatever-name": []byte("the-only-key"),
		},
	}

	data, err := sp.getKeyFromSecret(secret, "")
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if string(data) != "the-only-key" {
		t.Errorf("Expected 'the-only-key', got '%s'", data)
	}
}

func TestGetKeyFromSecret_NoMatch(t *testing.T) {
	sp := &SecretProvider{}
	secret := &corev1.Secret{
		Data: map[string][]byte{
			"key-a": []byte("a"),
			"key-b": []byte("b"),
		},
	}

	_, err := sp.getKeyFromSecret(secret, "")
	if err == nil {
		t.Error("Expected error when no suitable key found")
	}
}

func TestGetKeyFromSecret_MissingSecretKey(t *testing.T) {
	sp := &SecretProvider{secretKey: "nonexistent"}
	secret := &corev1.Secret{
		Data: map[string][]byte{
			"actual-key": []byte("data"),
		},
	}

	_, err := sp.getKeyFromSecret(secret, "")
	if err == nil {
		t.Error("Expected error when configured secretKey doesn't exist")
	}
}
