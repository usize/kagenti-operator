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
	"fmt"

	agentv1alpha1 "github.com/kagenti/operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var (
	secretLogger = ctrl.Log.WithName("signature").WithName("secret")
)

// SecretProvider verifies JWS signatures using public keys stored in Kubernetes Secrets
type SecretProvider struct {
	client          client.Client
	secretName      string
	secretNamespace string
	secretKey       string
	auditMode       bool
}

// NewSecretProvider creates a new Secret-based signature verification provider
func NewSecretProvider(config *Config) (Provider, error) {
	if config.SecretName == "" {
		return nil, fmt.Errorf("secret name is required")
	}
	if config.SecretNamespace == "" {
		return nil, fmt.Errorf("secret namespace is required")
	}

	return &SecretProvider{
		secretName:      config.SecretName,
		secretNamespace: config.SecretNamespace,
		secretKey:       config.SecretKey,
		auditMode:       config.AuditMode,
	}, nil
}

// SetClient sets the Kubernetes client (called after provider creation)
func (p *SecretProvider) SetClient(c client.Client) {
	p.client = c
}

// VerifySignature verifies JWS signatures using public keys from a Kubernetes Secret.
// Iterates over the signatures array; returns success on the first verified signature.
func (p *SecretProvider) VerifySignature(ctx context.Context, cardData *agentv1alpha1.AgentCardData, signatures []agentv1alpha1.AgentCardSignature) (*VerificationResult, error) {
	secretLogger.Info("Verifying JWS signature using Kubernetes Secret",
		"secret", p.secretName,
		"namespace", p.secretNamespace)

	if p.client == nil {
		return &VerificationResult{
			Verified: false,
			Error:    fmt.Errorf("kubernetes client not initialized"),
			Details:  "Internal error: client not set",
		}, fmt.Errorf("kubernetes client not initialized")
	}

	if len(signatures) == 0 {
		result := &VerificationResult{
			Verified: false,
			Details:  "AgentCard does not contain any signatures",
		}
		if p.auditMode {
			secretLogger.Info("Audit mode: AgentCard has no signatures, allowing anyway", "card", cardData.Name)
			result.Verified = true
			result.Details = "AgentCard has no signatures (audit mode: allowed)"
			return result, nil
		}
		result.Error = fmt.Errorf("no signatures provided")
		return result, nil
	}

	// Fetch the secret containing public keys
	secret := &corev1.Secret{}
	err := p.client.Get(ctx, types.NamespacedName{
		Name:      p.secretName,
		Namespace: p.secretNamespace,
	}, secret)
	if err != nil {
		if p.auditMode {
			secretLogger.Error(err, "Audit mode: Failed to fetch secret, allowing anyway")
			return &VerificationResult{
				Verified: true,
				Details:  fmt.Sprintf("Failed to fetch secret (audit mode: allowed): %v", err),
			}, nil
		}
		return &VerificationResult{
			Verified: false,
			Error:    err,
			Details:  fmt.Sprintf("Failed to fetch secret: %v", err),
		}, err
	}

	// Try each signature in the array
	for i := range signatures {
		sig := &signatures[i]

		// Decode protected header to extract kid
		header, headerErr := DecodeProtectedHeader(sig.Protected)
		if headerErr != nil {
			secretLogger.Info("Skipping signature with invalid protected header",
				"index", i, "error", headerErr)
			continue
		}

		// Try to find the key by kid from the protected header
		kid := header.KeyID
		keyData, keyErr := p.getKeyFromSecret(secret, kid)
		if keyErr == nil {
			result, verifyErr := VerifyJWS(cardData, sig, keyData)
			if verifyErr == nil && result != nil && result.Verified {
				return result, nil
			}
			secretLogger.Info("Verification failed with matched key, trying fallback",
				"keyID", kid, "error", verifyErr)
		}

		// Fallback: try all keys in the secret
		for keyName, data := range secret.Data {
			if keyErr == nil && string(data) == string(keyData) {
				continue // skip the key we already tried
			}
			result, verifyErr := VerifyJWS(cardData, sig, data)
			if verifyErr == nil && result != nil && result.Verified {
				secretLogger.Info("Signature verified with fallback key",
					"requestedKeyID", kid,
					"matchedKey", keyName)
				result.KeyID = keyName
				return result, nil
			}
		}
	}

	// No signature verified
	err = fmt.Errorf("JWS signature verification failed with all available keys")
	if p.auditMode {
		secretLogger.Error(err, "Audit mode: Signature verification failed, allowing anyway")
		return &VerificationResult{
			Verified: true,
			Details:  fmt.Sprintf("Signature verification failed (audit mode: allowed): %v", err),
		}, nil
	}
	return &VerificationResult{
		Verified: false,
		Error:    err,
		Details:  err.Error(),
	}, err
}

// getKeyFromSecret retrieves the appropriate public key from the secret
func (p *SecretProvider) getKeyFromSecret(secret *corev1.Secret, keyID string) ([]byte, error) {
	// If a specific key is configured, use that
	if p.secretKey != "" {
		if data, ok := secret.Data[p.secretKey]; ok {
			return data, nil
		}
		return nil, fmt.Errorf("key %s not found in secret", p.secretKey)
	}

	// If keyID is specified in the signature, try to find it
	if keyID != "" {
		if data, ok := secret.Data[keyID]; ok {
			return data, nil
		}
		// Also try with common extensions
		for _, ext := range []string{".pem", ".pub", ".key"} {
			if data, ok := secret.Data[keyID+ext]; ok {
				return data, nil
			}
		}
		return nil, fmt.Errorf("key with ID %s not found in secret", keyID)
	}

	// Try common key names
	for _, keyName := range []string{"public.pem", "publickey.pem", "key.pem", "public-key"} {
		if data, ok := secret.Data[keyName]; ok {
			return data, nil
		}
	}

	// If only one key in secret, use that
	if len(secret.Data) == 1 {
		for _, data := range secret.Data {
			return data, nil
		}
	}

	return nil, fmt.Errorf("no suitable public key found in secret")
}

// Name returns the provider name
func (p *SecretProvider) Name() string {
	return "secret"
}
