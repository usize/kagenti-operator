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
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"testing"

	agentv1alpha1 "github.com/kagenti/operator/api/v1alpha1"
)

// --- Test helpers ---

// generateRSAKeyPair generates an RSA key pair and returns PEM-encoded public key.
func generateRSAKeyPair(t *testing.T) (*rsa.PrivateKey, []byte) {
	t.Helper()
	privKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("Failed to generate RSA key: %v", err)
	}
	pubKeyDER, err := x509.MarshalPKIXPublicKey(&privKey.PublicKey)
	if err != nil {
		t.Fatalf("Failed to marshal RSA public key: %v", err)
	}
	pubKeyPEM := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubKeyDER})
	return privKey, pubKeyPEM
}

// generateECDSAKeyPair generates an ECDSA key pair and returns PEM-encoded public key.
func generateECDSAKeyPair(t *testing.T) (*ecdsa.PrivateKey, []byte) {
	t.Helper()
	privKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("Failed to generate ECDSA key: %v", err)
	}
	pubKeyDER, err := x509.MarshalPKIXPublicKey(&privKey.PublicKey)
	if err != nil {
		t.Fatalf("Failed to marshal ECDSA public key: %v", err)
	}
	pubKeyPEM := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubKeyDER})
	return privKey, pubKeyPEM
}

// buildJWSSignature creates a JWS signature for testing.
// It builds a protected header, constructs the signing input per RFC 7515,
// and signs with the given RSA private key.
func buildJWSSignature(t *testing.T, cardData *agentv1alpha1.AgentCardData, privKey *rsa.PrivateKey, kid, spiffeID string) agentv1alpha1.AgentCardSignature {
	t.Helper()
	header := &ProtectedHeader{
		Algorithm: "RS256",
		Type:      "JOSE",
		KeyID:     kid,
		SpiffeID:  spiffeID,
	}
	protectedB64, err := EncodeProtectedHeader(header)
	if err != nil {
		t.Fatalf("Failed to encode protected header: %v", err)
	}

	payload, err := createCanonicalCardJSON(cardData)
	if err != nil {
		t.Fatalf("Failed to create canonical JSON: %v", err)
	}
	payloadB64 := base64.RawURLEncoding.EncodeToString(payload)
	signingInput := []byte(protectedB64 + "." + payloadB64)

	hash := sha256.Sum256(signingInput)
	sigBytes, err := rsa.SignPKCS1v15(rand.Reader, privKey, crypto.SHA256, hash[:])
	if err != nil {
		t.Fatalf("Failed to sign: %v", err)
	}

	return agentv1alpha1.AgentCardSignature{
		Protected: protectedB64,
		Signature: base64.RawURLEncoding.EncodeToString(sigBytes),
	}
}

// buildJWSSignatureECDSA creates a JWS signature using ECDSA (ASN.1 DER, for fallback compat).
func buildJWSSignatureECDSA(t *testing.T, cardData *agentv1alpha1.AgentCardData, privKey *ecdsa.PrivateKey, kid string) agentv1alpha1.AgentCardSignature {
	t.Helper()
	header := &ProtectedHeader{
		Algorithm: "ES256",
		Type:      "JOSE",
		KeyID:     kid,
	}
	protectedB64, err := EncodeProtectedHeader(header)
	if err != nil {
		t.Fatalf("Failed to encode protected header: %v", err)
	}

	payload, err := createCanonicalCardJSON(cardData)
	if err != nil {
		t.Fatalf("Failed to create canonical JSON: %v", err)
	}
	payloadB64 := base64.RawURLEncoding.EncodeToString(payload)
	signingInput := []byte(protectedB64 + "." + payloadB64)

	hash := sha256.Sum256(signingInput)
	sigBytes, err := ecdsa.SignASN1(rand.Reader, privKey, hash[:])
	if err != nil {
		t.Fatalf("Failed to sign: %v", err)
	}

	return agentv1alpha1.AgentCardSignature{
		Protected: protectedB64,
		Signature: base64.RawURLEncoding.EncodeToString(sigBytes),
	}
}

// newCardData creates a simple AgentCardData for testing.
func newCardData(name, url, version string) *agentv1alpha1.AgentCardData {
	return &agentv1alpha1.AgentCardData{
		Name:    name,
		URL:     url,
		Version: version,
	}
}

// --- ProtectedHeader tests ---

func TestDecodeProtectedHeader(t *testing.T) {
	header := &ProtectedHeader{
		Algorithm: "RS256",
		KeyID:     "test-key",
		SpiffeID:  "spiffe://cluster.local/ns/default/sa/agent",
	}
	encoded, err := EncodeProtectedHeader(header)
	if err != nil {
		t.Fatalf("EncodeProtectedHeader failed: %v", err)
	}

	decoded, err := DecodeProtectedHeader(encoded)
	if err != nil {
		t.Fatalf("DecodeProtectedHeader failed: %v", err)
	}
	if decoded.Algorithm != "RS256" {
		t.Errorf("Expected alg=RS256, got %s", decoded.Algorithm)
	}
	if decoded.KeyID != "test-key" {
		t.Errorf("Expected kid=test-key, got %s", decoded.KeyID)
	}
	if decoded.SpiffeID != "spiffe://cluster.local/ns/default/sa/agent" {
		t.Errorf("Expected spiffe_id match, got %s", decoded.SpiffeID)
	}
}

func TestDecodeProtectedHeader_InvalidBase64(t *testing.T) {
	_, err := DecodeProtectedHeader("not-valid-base64!!!")
	if err == nil {
		t.Error("Expected error for invalid base64url")
	}
}

func TestDecodeProtectedHeader_InvalidJSON(t *testing.T) {
	encoded := base64.RawURLEncoding.EncodeToString([]byte("not json"))
	_, err := DecodeProtectedHeader(encoded)
	if err == nil {
		t.Error("Expected error for invalid JSON")
	}
}

// --- Canonical JSON tests ---

func TestCanonicalJSON_SortedKeys(t *testing.T) {
	cardData := &agentv1alpha1.AgentCardData{
		Name:    "Test Agent",
		URL:     "http://localhost:8000",
		Version: "1.0.0",
	}

	canonical, err := createCanonicalCardJSON(cardData)
	if err != nil {
		t.Fatalf("createCanonicalCardJSON failed: %v", err)
	}

	// Verify it's valid JSON
	var parsed map[string]interface{}
	if err := json.Unmarshal(canonical, &parsed); err != nil {
		t.Fatalf("Canonical JSON is not valid JSON: %v", err)
	}

	// Verify no structural whitespace (newlines/tabs)
	for _, b := range canonical {
		if b == '\n' || b == '\t' {
			t.Fatalf("Canonical JSON should not contain newlines or tabs: %s", canonical)
		}
	}

	// Verify keys are sorted
	got := string(canonical)
	nameIdx := indexOf(got, `"name"`)
	urlIdx := indexOf(got, `"url"`)
	versionIdx := indexOf(got, `"version"`)

	if nameIdx >= urlIdx || urlIdx >= versionIdx {
		t.Errorf("Keys not sorted: name@%d, url@%d, version@%d in %s", nameIdx, urlIdx, versionIdx, got)
	}
}

func TestCanonicalJSON_ExcludesSignatures(t *testing.T) {
	cardData := &agentv1alpha1.AgentCardData{
		Name:    "Test Agent",
		Version: "1.0.0",
		Signatures: []agentv1alpha1.AgentCardSignature{
			{Protected: "abc", Signature: "should-be-excluded"},
		},
	}

	canonical, err := createCanonicalCardJSON(cardData)
	if err != nil {
		t.Fatalf("createCanonicalCardJSON failed: %v", err)
	}

	got := string(canonical)
	if indexOf(got, "signatures") >= 0 {
		t.Errorf("Canonical JSON should NOT contain 'signatures' field: %s", got)
	}
	if indexOf(got, "should-be-excluded") >= 0 {
		t.Errorf("Canonical JSON should NOT contain signature value: %s", got)
	}
}

func TestCanonicalJSON_ExcludesEmptyFields(t *testing.T) {
	cardData := &agentv1alpha1.AgentCardData{
		Name:    "Test Agent",
		Version: "1.0.0",
	}

	canonical, err := createCanonicalCardJSON(cardData)
	if err != nil {
		t.Fatalf("createCanonicalCardJSON failed: %v", err)
	}

	got := string(canonical)
	if indexOf(got, `"url"`) >= 0 {
		t.Errorf("Canonical JSON should not include empty 'url' field: %s", got)
	}
	if indexOf(got, `"description"`) >= 0 {
		t.Errorf("Canonical JSON should not include empty 'description' field: %s", got)
	}
}

func TestCanonicalJSON_Deterministic(t *testing.T) {
	cardData := &agentv1alpha1.AgentCardData{
		Name:               "Agent",
		Description:        "A test agent",
		Version:            "2.0.0",
		URL:                "http://localhost:9000",
		DefaultInputModes:  []string{"text/plain", "application/json"},
		DefaultOutputModes: []string{"application/json"},
	}

	first, err := createCanonicalCardJSON(cardData)
	if err != nil {
		t.Fatalf("First call failed: %v", err)
	}

	for i := 0; i < 10; i++ {
		again, err := createCanonicalCardJSON(cardData)
		if err != nil {
			t.Fatalf("Call %d failed: %v", i, err)
		}
		if string(first) != string(again) {
			t.Fatalf("Canonical JSON is non-deterministic:\n  first: %s\n  got:   %s", first, again)
		}
	}
}

func TestCanonicalJSON_NestedCapabilities(t *testing.T) {
	streaming := true
	push := false
	cardData := &agentv1alpha1.AgentCardData{
		Name: "Agent",
		Capabilities: &agentv1alpha1.AgentCapabilities{
			Streaming:         &streaming,
			PushNotifications: &push,
		},
	}

	canonical, err := createCanonicalCardJSON(cardData)
	if err != nil {
		t.Fatalf("createCanonicalCardJSON failed: %v", err)
	}

	got := string(canonical)
	pushIdx := indexOf(got, `"pushNotifications"`)
	streamIdx := indexOf(got, `"streaming"`)
	if pushIdx < 0 || streamIdx < 0 {
		t.Fatalf("Missing expected nested keys in: %s", got)
	}
	if pushIdx >= streamIdx {
		t.Errorf("Nested keys not sorted: pushNotifications@%d >= streaming@%d in %s", pushIdx, streamIdx, got)
	}
}

// --- JWS RSA signature verification tests ---

func TestVerifyJWS_RSA_ValidSignature(t *testing.T) {
	privKey, pubKeyPEM := generateRSAKeyPair(t)
	cardData := newCardData("Weather Agent", "http://weather:8000", "1.0.0")
	jwsSig := buildJWSSignature(t, cardData, privKey, "test-key", "")

	result, err := VerifyJWS(cardData, &jwsSig, pubKeyPEM)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if !result.Verified {
		t.Errorf("Expected verified=true, got false. Details: %s", result.Details)
	}
	if result.KeyID != "test-key" {
		t.Errorf("Expected keyID=test-key, got %s", result.KeyID)
	}
}

func TestVerifyJWS_RSA_WithSpiffeID(t *testing.T) {
	privKey, pubKeyPEM := generateRSAKeyPair(t)
	cardData := newCardData("Agent", "http://agent:8000", "1.0.0")
	spiffeID := "spiffe://cluster.local/ns/default/sa/agent-sa"
	jwsSig := buildJWSSignature(t, cardData, privKey, "key-1", spiffeID)

	result, err := VerifyJWS(cardData, &jwsSig, pubKeyPEM)
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

func TestVerifyJWS_RSA_WrongKey(t *testing.T) {
	privKey, _ := generateRSAKeyPair(t)
	_, wrongPubKeyPEM := generateRSAKeyPair(t)

	cardData := newCardData("Agent", "http://agent:8000", "1.0.0")
	jwsSig := buildJWSSignature(t, cardData, privKey, "wrong-key", "")

	result, err := VerifyJWS(cardData, &jwsSig, wrongPubKeyPEM)
	if err != nil {
		t.Fatalf("Unexpected error (should return result with Verified=false): %v", err)
	}
	if result.Verified {
		t.Error("Expected verified=false for wrong key, got true")
	}
}

func TestVerifyJWS_RSA_TamperedCard(t *testing.T) {
	privKey, pubKeyPEM := generateRSAKeyPair(t)

	original := newCardData("Agent", "http://agent:8000", "1.0.0")
	jwsSig := buildJWSSignature(t, original, privKey, "key-1", "")

	// Tamper with card data after signing
	tampered := newCardData("Agent", "http://evil:8000", "1.0.0")

	result, err := VerifyJWS(tampered, &jwsSig, pubKeyPEM)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if result.Verified {
		t.Error("Expected verified=false for tampered card, got true")
	}
}

// --- JWS ECDSA signature verification tests ---

func TestVerifyJWS_ECDSA_ValidSignature(t *testing.T) {
	privKey, pubKeyPEM := generateECDSAKeyPair(t)
	cardData := newCardData("ECDSA Agent", "http://ecdsa:8000", "1.0.0")
	jwsSig := buildJWSSignatureECDSA(t, cardData, privKey, "ecdsa-key")

	result, err := VerifyJWS(cardData, &jwsSig, pubKeyPEM)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if !result.Verified {
		t.Errorf("Expected verified=true, got false. Details: %s", result.Details)
	}
}

func TestVerifyJWS_ECDSA_WrongKey(t *testing.T) {
	privKey, _ := generateECDSAKeyPair(t)
	_, wrongPubKeyPEM := generateECDSAKeyPair(t)

	cardData := newCardData("Agent", "http://agent:8000", "1.0.0")
	jwsSig := buildJWSSignatureECDSA(t, cardData, privKey, "wrong-key")

	result, err := VerifyJWS(cardData, &jwsSig, wrongPubKeyPEM)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if result.Verified {
		t.Error("Expected verified=false for wrong key, got true")
	}
}

// --- Edge case tests ---

func TestVerifyJWS_NilSignature(t *testing.T) {
	_, pubKeyPEM := generateRSAKeyPair(t)
	cardData := newCardData("Agent", "http://agent:8000", "1.0.0")

	result, err := VerifyJWS(cardData, nil, pubKeyPEM)
	if err != nil {
		t.Fatalf("Unexpected error for nil signature: %v", err)
	}
	if result.Verified {
		t.Error("Expected verified=false for nil signature, got true")
	}
	if indexOf(result.Details, "does not contain a signature") < 0 {
		t.Errorf("Expected details to mention missing signature, got: %s", result.Details)
	}
}

func TestVerifyJWS_InvalidProtectedHeader(t *testing.T) {
	_, pubKeyPEM := generateRSAKeyPair(t)
	cardData := newCardData("Agent", "http://agent:8000", "1.0.0")

	sig := &agentv1alpha1.AgentCardSignature{
		Protected: "not-valid-base64!!!",
		Signature: "fake",
	}

	_, err := VerifyJWS(cardData, sig, pubKeyPEM)
	if err == nil {
		t.Error("Expected error for invalid protected header")
	}
}

func TestVerifyJWS_InvalidPEM(t *testing.T) {
	privKey, _ := generateRSAKeyPair(t)
	cardData := newCardData("Agent", "http://agent:8000", "1.0.0")
	jwsSig := buildJWSSignature(t, cardData, privKey, "key-1", "")

	invalidPEM := []byte("not a PEM block")
	result, err := VerifyJWS(cardData, &jwsSig, invalidPEM)
	if err == nil {
		t.Error("Expected error for invalid PEM")
	}
	if result.Verified {
		t.Error("Expected verified=false for invalid PEM")
	}
}

func TestVerifyJWS_InvalidSignatureBase64(t *testing.T) {
	_, pubKeyPEM := generateRSAKeyPair(t)
	cardData := newCardData("Agent", "http://agent:8000", "1.0.0")

	header := &ProtectedHeader{Algorithm: "RS256", KeyID: "k"}
	protectedB64, _ := EncodeProtectedHeader(header)

	sig := &agentv1alpha1.AgentCardSignature{
		Protected: protectedB64,
		Signature: "not-valid-base64!!!",
	}

	_, err := VerifyJWS(cardData, sig, pubKeyPEM)
	if err == nil {
		t.Error("Expected error for invalid base64url signature")
	}
}

func TestVerifyJWS_PreservesKeyID(t *testing.T) {
	privKey, pubKeyPEM := generateRSAKeyPair(t)
	cardData := newCardData("Agent", "http://agent:8000", "1.0.0")
	jwsSig := buildJWSSignature(t, cardData, privKey, "my-special-key-id", "")

	result, _ := VerifyJWS(cardData, &jwsSig, pubKeyPEM)
	if result.KeyID != "my-special-key-id" {
		t.Errorf("Expected keyID='my-special-key-id', got '%s'", result.KeyID)
	}
}

// --- Cross-algorithm compatibility test ---

func TestVerifyJWS_RSAKeyWithECDSASignature(t *testing.T) {
	ecPriv, _ := generateECDSAKeyPair(t)
	_, rsaPubPEM := generateRSAKeyPair(t)

	cardData := newCardData("Agent", "http://agent:8000", "1.0.0")
	jwsSig := buildJWSSignatureECDSA(t, cardData, ecPriv, "ec-key")

	// Try verifying ECDSA signature with RSA public key — should fail with algorithm mismatch
	result, err := VerifyJWS(cardData, &jwsSig, rsaPubPEM)
	if err == nil {
		t.Fatal("Expected algorithm mismatch error when using RSA key with ES256 header")
	}
	if result.Verified {
		t.Error("Expected verified=false when using RSA key to verify ECDSA signature")
	}
	if indexOf(err.Error(), "algorithm mismatch") == -1 {
		t.Errorf("Expected 'algorithm mismatch' in error, got: %v", err)
	}
}

// TestVerifyJWS_AlgNone_Rejected verifies that "alg: none" is explicitly rejected.
// This prevents CVE-2015-9235 (the classic JWS "alg: none" attack).
func TestVerifyJWS_AlgNone_Rejected(t *testing.T) {
	_, rsaPubPEM := generateRSAKeyPair(t)
	cardData := newCardData("Agent", "http://agent:8000", "1.0.0")

	// Craft a signature with alg: "none"
	header := &ProtectedHeader{
		Algorithm: "none",
		Type:      "JOSE",
		KeyID:     "attack-key",
	}
	headerJSON, _ := json.Marshal(header)
	protectedB64 := base64.RawURLEncoding.EncodeToString(headerJSON)

	sig := agentv1alpha1.AgentCardSignature{
		Protected: protectedB64,
		Signature: base64.RawURLEncoding.EncodeToString([]byte("fake-signature")),
	}

	result, err := VerifyJWS(cardData, &sig, rsaPubPEM)
	if err == nil {
		t.Fatal("Expected error when alg is 'none'")
	}
	if result.Verified {
		t.Error("Expected verified=false when alg is 'none'")
	}
	if indexOf(err.Error(), "not permitted") == -1 {
		t.Errorf("Expected 'not permitted' in error, got: %v", err)
	}
}

// TestVerifyJWS_EmptyAlg_Rejected verifies that missing alg is rejected.
func TestVerifyJWS_EmptyAlg_Rejected(t *testing.T) {
	_, rsaPubPEM := generateRSAKeyPair(t)
	cardData := newCardData("Agent", "http://agent:8000", "1.0.0")

	// Craft a signature with empty alg
	header := &ProtectedHeader{
		Type:  "JOSE",
		KeyID: "some-key",
	}
	headerJSON, _ := json.Marshal(header)
	protectedB64 := base64.RawURLEncoding.EncodeToString(headerJSON)

	sig := agentv1alpha1.AgentCardSignature{
		Protected: protectedB64,
		Signature: base64.RawURLEncoding.EncodeToString([]byte("fake-signature")),
	}

	result, err := VerifyJWS(cardData, &sig, rsaPubPEM)
	if err == nil {
		t.Fatal("Expected error when alg is empty")
	}
	if result.Verified {
		t.Error("Expected verified=false when alg is empty")
	}
}

// TestVerifyJWS_UnsupportedAlg_Rejected verifies that unknown algorithms are rejected.
func TestVerifyJWS_UnsupportedAlg_Rejected(t *testing.T) {
	_, rsaPubPEM := generateRSAKeyPair(t)
	cardData := newCardData("Agent", "http://agent:8000", "1.0.0")

	header := &ProtectedHeader{
		Algorithm: "HS256", // HMAC — not supported (and dangerous if accepted with asymmetric keys)
		Type:      "JOSE",
		KeyID:     "some-key",
	}
	headerJSON, _ := json.Marshal(header)
	protectedB64 := base64.RawURLEncoding.EncodeToString(headerJSON)

	sig := agentv1alpha1.AgentCardSignature{
		Protected: protectedB64,
		Signature: base64.RawURLEncoding.EncodeToString([]byte("fake-signature")),
	}

	result, err := VerifyJWS(cardData, &sig, rsaPubPEM)
	if err == nil {
		t.Fatal("Expected error when alg is unsupported (HS256)")
	}
	if result.Verified {
		t.Error("Expected verified=false when alg is unsupported")
	}
}

// --- Helper ---

func indexOf(s, substr string) int {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}
