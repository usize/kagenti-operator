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
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"sort"

	agentv1alpha1 "github.com/kagenti/operator/api/v1alpha1"
)

// ProtectedHeader represents the decoded JWS protected header.
// Per A2A spec section 8.4.2, the protected header MUST contain:
//   - alg: the signature algorithm (e.g. "RS256", "ES256")
//   - typ: SHOULD be "JOSE" for JWS
//   - kid: the key identifier
//
// And MAY contain:
//   - jku: URL to JWKS containing the public key
//   - spiffe_id: SPIFFE identity of the signer (extension for identity binding)
type ProtectedHeader struct {
	Algorithm string `json:"alg"`
	Type      string `json:"typ,omitempty"`
	KeyID     string `json:"kid,omitempty"`
	JWKSURL   string `json:"jku,omitempty"`
	SpiffeID  string `json:"spiffe_id,omitempty"`
}

// DecodeProtectedHeader decodes a base64url-encoded JWS protected header.
func DecodeProtectedHeader(protected string) (*ProtectedHeader, error) {
	headerJSON, err := base64.RawURLEncoding.DecodeString(protected)
	if err != nil {
		return nil, fmt.Errorf("failed to decode protected header: %w", err)
	}
	var header ProtectedHeader
	if err := json.Unmarshal(headerJSON, &header); err != nil {
		return nil, fmt.Errorf("failed to parse protected header: %w", err)
	}
	return &header, nil
}

// EncodeProtectedHeader encodes a ProtectedHeader to a base64url string.
func EncodeProtectedHeader(header *ProtectedHeader) (string, error) {
	headerJSON, err := json.Marshal(header)
	if err != nil {
		return "", fmt.Errorf("failed to marshal protected header: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(headerJSON), nil
}

// VerifyJWS verifies a single JWS signature against card data using a public key (PEM format).
// This follows A2A spec section 8.4.3:
//
//	signingInput = BASE64URL(UTF8(JWS Protected Header)) || '.' || BASE64URL(JWS Payload)
//	where JWS Payload = canonical JSON of card data excluding "signatures"
func VerifyJWS(cardData *agentv1alpha1.AgentCardData, sig *agentv1alpha1.AgentCardSignature, publicKeyPEM []byte) (*VerificationResult, error) {
	if sig == nil {
		return &VerificationResult{
			Verified: false,
			Error:    fmt.Errorf("no signature provided"),
			Details:  "AgentCard does not contain a signature",
		}, nil
	}

	// Decode the protected header to extract alg, kid, spiffe_id
	header, err := DecodeProtectedHeader(sig.Protected)
	if err != nil {
		return &VerificationResult{
			Verified: false,
			Error:    err,
			Details:  fmt.Sprintf("Failed to decode JWS protected header: %v", err),
		}, err
	}

	// Validate algorithm — reject "none" and unsupported algorithms.
	// Per RFC 7515 §5.2, the recipient MUST validate that it understands the algorithm.
	// This prevents the classic "alg: none" attack (CVE-2015-9235).
	if err := validateAlgorithm(header.Algorithm); err != nil {
		return &VerificationResult{
			Verified: false,
			Error:    err,
			Details:  fmt.Sprintf("Algorithm validation failed: %v", err),
		}, err
	}

	// Parse the public key
	block, _ := pem.Decode(publicKeyPEM)
	if block == nil {
		return &VerificationResult{
			Verified: false,
			Error:    fmt.Errorf("failed to decode PEM block"),
			Details:  "Invalid PEM format",
		}, fmt.Errorf("failed to decode PEM block")
	}

	publicKey, err := parsePublicKey(block.Bytes)
	if err != nil {
		return &VerificationResult{
			Verified: false,
			Error:    err,
			Details:  fmt.Sprintf("Failed to parse public key: %v", err),
		}, err
	}

	// Create canonical payload (card JSON excluding "signatures")
	payload, err := createCanonicalCardJSON(cardData)
	if err != nil {
		return &VerificationResult{
			Verified: false,
			Error:    err,
			Details:  fmt.Sprintf("Failed to create canonical JSON payload: %v", err),
		}, err
	}

	// Construct JWS signing input per RFC 7515:
	//   ASCII(BASE64URL(UTF8(JWS Protected Header))) || '.' || ASCII(BASE64URL(JWS Payload))
	payloadB64 := base64.RawURLEncoding.EncodeToString(payload)
	signingInput := []byte(sig.Protected + "." + payloadB64)

	// Hash the signing input
	hash := sha256.Sum256(signingInput)

	// Decode the signature value (base64url, no padding)
	signatureBytes, err := base64.RawURLEncoding.DecodeString(sig.Signature)
	if err != nil {
		return &VerificationResult{
			Verified: false,
			Error:    err,
			Details:  "Failed to decode JWS signature from base64url",
		}, err
	}

	// Verify based on key type and algorithm.
	// Also validate that the header alg matches the actual key type to prevent
	// algorithm confusion attacks (e.g., header says ES256 but key is RSA).
	var verified bool
	switch pub := publicKey.(type) {
	case *rsa.PublicKey:
		if !isRSAAlgorithm(header.Algorithm) {
			return &VerificationResult{
				Verified: false,
				Error:    fmt.Errorf("algorithm mismatch: header alg=%q but key is RSA", header.Algorithm),
				Details:  fmt.Sprintf("Algorithm mismatch: protected header specifies %q but public key is RSA (expected RS256/RS384/RS512/PS256/PS384/PS512)", header.Algorithm),
			}, fmt.Errorf("algorithm mismatch: header alg=%q but key is RSA", header.Algorithm)
		}
		err = rsa.VerifyPKCS1v15(pub, crypto.SHA256, hash[:], signatureBytes)
		verified = (err == nil)
	case *ecdsa.PublicKey:
		if !isECDSAAlgorithm(header.Algorithm) {
			return &VerificationResult{
				Verified: false,
				Error:    fmt.Errorf("algorithm mismatch: header alg=%q but key is ECDSA", header.Algorithm),
				Details:  fmt.Sprintf("Algorithm mismatch: protected header specifies %q but public key is ECDSA (expected ES256/ES384/ES512)", header.Algorithm),
			}, fmt.Errorf("algorithm mismatch: header alg=%q but key is ECDSA", header.Algorithm)
		}
		// JWS ES256/ES384/ES512 uses raw R||S encoding (not ASN.1 DER).
		// Try raw R||S first (spec-compliant), fall back to ASN.1 DER.
		verified = verifyECDSARaw(pub, hash[:], signatureBytes)
		if !verified {
			// Fallback: try ASN.1 DER encoding for backward compatibility
			verified = ecdsa.VerifyASN1(pub, hash[:], signatureBytes)
		}
	default:
		return &VerificationResult{
			Verified: false,
			Error:    fmt.Errorf("unsupported key type"),
			Details:  "Public key type not supported (expected RSA or ECDSA)",
		}, fmt.Errorf("unsupported key type")
	}

	result := &VerificationResult{
		Verified: verified,
		KeyID:    header.KeyID,
		SpiffeID: header.SpiffeID,
	}

	if !verified {
		result.Details = "JWS signature verification failed"
		result.Error = fmt.Errorf("JWS signature verification failed")
	} else {
		result.Details = fmt.Sprintf("JWS signature verified successfully (alg=%s, kid=%s)", header.Algorithm, header.KeyID)
	}

	return result, nil
}

// verifyECDSARaw verifies an ECDSA signature in JWS raw R||S format.
// Per RFC 7518 section 3.4, ES256 signatures are 64 bytes (32 + 32),
// ES384 are 96 bytes, ES512 are 132 bytes.
func verifyECDSARaw(pub *ecdsa.PublicKey, hash, sig []byte) bool {
	keySize := curveByteSize(pub.Curve)
	if len(sig) != 2*keySize {
		return false
	}

	r := new(big.Int).SetBytes(sig[:keySize])
	s := new(big.Int).SetBytes(sig[keySize:])
	return ecdsa.Verify(pub, hash, r, s)
}

// curveByteSize returns the byte size for a curve's field elements.
func curveByteSize(curve elliptic.Curve) int {
	bitSize := curve.Params().BitSize
	return (bitSize + 7) / 8
}

// supportedAlgorithms is the set of JWS algorithms we accept.
// Per RFC 7515 §5.2, verifiers MUST reject algorithms they don't support.
var supportedAlgorithms = map[string]bool{
	"RS256": true, "RS384": true, "RS512": true,
	"PS256": true, "PS384": true, "PS512": true,
	"ES256": true, "ES384": true, "ES512": true,
}

// validateAlgorithm rejects "none" and any algorithm we don't support.
// This prevents the classic "alg: none" attack (CVE-2015-9235) and ensures
// we only accept algorithms whose verification logic is implemented.
func validateAlgorithm(alg string) error {
	if alg == "" {
		return fmt.Errorf("JWS protected header missing required 'alg' field")
	}
	if alg == "none" {
		return fmt.Errorf("JWS algorithm 'none' is not permitted — signatures must be cryptographically verified")
	}
	if !supportedAlgorithms[alg] {
		return fmt.Errorf("unsupported JWS algorithm %q (supported: RS256, RS384, RS512, PS256, PS384, PS512, ES256, ES384, ES512)", alg)
	}
	return nil
}

// isRSAAlgorithm returns true if the JWS algorithm corresponds to an RSA key.
func isRSAAlgorithm(alg string) bool {
	switch alg {
	case "RS256", "RS384", "RS512", "PS256", "PS384", "PS512":
		return true
	}
	return false
}

// isECDSAAlgorithm returns true if the JWS algorithm corresponds to an ECDSA key.
func isECDSAAlgorithm(alg string) bool {
	switch alg {
	case "ES256", "ES384", "ES512":
		return true
	}
	return false
}

// parsePublicKey parses a public key from DER format
func parsePublicKey(derBytes []byte) (crypto.PublicKey, error) {
	// Try parsing as PKIX first
	if key, err := x509.ParsePKIXPublicKey(derBytes); err == nil {
		return key, nil
	}

	// Try parsing as PKCS1 RSA public key
	if key, err := x509.ParsePKCS1PublicKey(derBytes); err == nil {
		return key, nil
	}

	return nil, fmt.Errorf("failed to parse public key")
}

// createCanonicalCardJSON creates a canonical JSON representation of the card.
// The payload for JWS signing: card JSON with "signatures" field excluded.
// Matches Python: json.dumps(card_data, sort_keys=True, separators=(',', ':'))
//
// Approach: marshal full struct → unmarshal to map → remove "signatures" → canonical JSON.
func createCanonicalCardJSON(cardData *agentv1alpha1.AgentCardData) ([]byte, error) {
	// Marshal the full struct to JSON
	rawJSON, err := json.Marshal(cardData)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal card data: %w", err)
	}

	// Unmarshal to generic map
	var cardMap map[string]interface{}
	if err := json.Unmarshal(rawJSON, &cardMap); err != nil {
		return nil, fmt.Errorf("failed to unmarshal to map: %w", err)
	}

	// Remove the signatures field — it must not be part of the signed payload
	delete(cardMap, "signatures")

	// Remove empty/nil fields to match Python behavior where absent fields are not included
	cleanMap := removeEmptyFields(cardMap)

	// Produce canonical JSON with sorted keys
	return marshalCanonical(cleanMap)
}

// removeEmptyFields removes nil values and empty collections from a map.
// This ensures the canonical JSON matches the Python implementation where
// absent fields are simply not included in the JSON output.
func removeEmptyFields(m map[string]interface{}) map[string]interface{} {
	result := make(map[string]interface{})
	for k, v := range m {
		if v == nil {
			continue
		}
		switch val := v.(type) {
		case map[string]interface{}:
			cleaned := removeEmptyFields(val)
			if len(cleaned) > 0 {
				result[k] = cleaned
			}
		case []interface{}:
			if len(val) > 0 {
				result[k] = val
			}
		case string:
			if val != "" {
				result[k] = val
			}
		default:
			result[k] = v
		}
	}
	return result
}

// marshalCanonical marshals a map to JSON with sorted keys and no whitespace.
// Produces output matching Python's json.dumps(sort_keys=True, separators=(',', ':'))
func marshalCanonical(data map[string]interface{}) ([]byte, error) {
	var buf bytes.Buffer

	keys := make([]string, 0, len(data))
	for k := range data {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	buf.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			buf.WriteByte(',')
		}
		keyJSON, err := json.Marshal(k)
		if err != nil {
			return nil, err
		}
		buf.Write(keyJSON)
		buf.WriteByte(':')
		valueJSON, err := marshalValue(data[k])
		if err != nil {
			return nil, err
		}
		buf.Write(valueJSON)
	}
	buf.WriteByte('}')

	return buf.Bytes(), nil
}

// marshalValue marshals a value with sorted keys if it's a map.
func marshalValue(v interface{}) ([]byte, error) {
	switch val := v.(type) {
	case map[string]interface{}:
		return marshalCanonical(val)
	case nil:
		return []byte("null"), nil
	case bool:
		if val {
			return []byte("true"), nil
		}
		return []byte("false"), nil
	case string:
		return json.Marshal(val)
	case float64, float32, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return json.Marshal(val)
	case []interface{}:
		return marshalArray(val)
	default:
		genericValue, err := toGenericValue(val)
		if err != nil {
			return nil, err
		}
		return marshalValue(genericValue)
	}
}

// toGenericValue converts any value to a generic JSON-compatible type.
func toGenericValue(v interface{}) (interface{}, error) {
	jsonBytes, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal value: %w", err)
	}
	var generic interface{}
	if err := json.Unmarshal(jsonBytes, &generic); err != nil {
		return nil, fmt.Errorf("failed to unmarshal to generic: %w", err)
	}
	return generic, nil
}

// marshalArray marshals an array with proper handling of nested objects.
func marshalArray(arr []interface{}) ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('[')
	for i, item := range arr {
		if i > 0 {
			buf.WriteByte(',')
		}
		itemJSON, err := marshalValue(item)
		if err != nil {
			return nil, err
		}
		buf.Write(itemJSON)
	}
	buf.WriteByte(']')
	return buf.Bytes(), nil
}
