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

package spiffe

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"strings"
	"testing"
	"time"
)

// generateTestCA creates a self-signed CA certificate for testing.
func generateTestCA(t *testing.T) (*x509.Certificate, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("failed to generate key: %v", err)
	}

	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "Test CA"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		IsCA:         true,
		KeyUsage:     x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}

	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("failed to create certificate: %v", err)
	}

	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("failed to parse certificate: %v", err)
	}

	return cert, der
}

func makeBundle(certs ...[]byte) string {
	x5c := make([]string, len(certs))
	for i, der := range certs {
		x5c[i] = base64.StdEncoding.EncodeToString(der)
	}
	bundle := spiffeBundleJSON{
		Keys: []spiffeBundleKey{
			{Use: "x509-svid", X5C: x5c},
		},
	}
	data, _ := json.Marshal(bundle)
	return string(data)
}

func TestParseTrustBundleToPEM(t *testing.T) {
	_, caDER := generateTestCA(t)

	t.Run("valid single cert", func(t *testing.T) {
		bundleJSON := makeBundle(caDER)
		pemData, err := ParseTrustBundleToPEM(bundleJSON)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Verify PEM decodes back to a valid certificate.
		block, rest := pem.Decode(pemData)
		if block == nil {
			t.Fatal("failed to decode PEM block")
		}
		if block.Type != "CERTIFICATE" {
			t.Errorf("expected CERTIFICATE block type, got %s", block.Type)
		}
		if _, err := x509.ParseCertificate(block.Bytes); err != nil {
			t.Fatalf("PEM contains invalid certificate: %v", err)
		}
		// Should be only one cert.
		if len(rest) != 0 {
			extraBlock, _ := pem.Decode(rest)
			if extraBlock != nil {
				t.Error("expected only one PEM block")
			}
		}
	})

	t.Run("multiple certs", func(t *testing.T) {
		_, ca2DER := generateTestCA(t)
		bundleJSON := makeBundle(caDER, ca2DER)
		pemData, err := ParseTrustBundleToPEM(bundleJSON)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Count PEM blocks.
		count := 0
		remaining := pemData
		for {
			var block *pem.Block
			block, remaining = pem.Decode(remaining)
			if block == nil {
				break
			}
			count++
		}
		if count != 2 {
			t.Errorf("expected 2 PEM blocks, got %d", count)
		}
	})

	t.Run("empty bundle", func(t *testing.T) {
		bundleJSON := `{"keys":[]}`
		_, err := ParseTrustBundleToPEM(bundleJSON)
		if err == nil {
			t.Fatal("expected error for empty bundle")
		}
		if !strings.Contains(err.Error(), "no x509-svid certificates") {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("invalid JSON", func(t *testing.T) {
		_, err := ParseTrustBundleToPEM("not json")
		if err == nil {
			t.Fatal("expected error for invalid JSON")
		}
	})

	t.Run("invalid base64", func(t *testing.T) {
		bundleJSON := `{"keys":[{"use":"x509-svid","x5c":["not-valid-base64!!!"]}]}`
		_, err := ParseTrustBundleToPEM(bundleJSON)
		if err == nil {
			t.Fatal("expected error for invalid base64")
		}
	})

	t.Run("invalid certificate DER", func(t *testing.T) {
		invalidDER := base64.StdEncoding.EncodeToString([]byte("not a certificate"))
		bundleJSON := `{"keys":[{"use":"x509-svid","x5c":["` + invalidDER + `"]}]}`
		_, err := ParseTrustBundleToPEM(bundleJSON)
		if err == nil {
			t.Fatal("expected error for invalid certificate DER")
		}
	})

	t.Run("skips non-x509-svid keys", func(t *testing.T) {
		bundle := spiffeBundleJSON{
			Keys: []spiffeBundleKey{
				{Use: "jwt-svid", X5C: []string{base64.StdEncoding.EncodeToString(caDER)}},
				{Use: "x509-svid", X5C: []string{base64.StdEncoding.EncodeToString(caDER)}},
			},
		}
		data, _ := json.Marshal(bundle)
		pemData, err := ParseTrustBundleToPEM(string(data))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Should only have one cert (from x509-svid, not jwt-svid).
		count := 0
		remaining := pemData
		for {
			var block *pem.Block
			block, remaining = pem.Decode(remaining)
			if block == nil {
				break
			}
			count++
		}
		if count != 1 {
			t.Errorf("expected 1 PEM block (only x509-svid), got %d", count)
		}
	})

	t.Run("only jwt-svid keys yields error", func(t *testing.T) {
		bundle := spiffeBundleJSON{
			Keys: []spiffeBundleKey{
				{Use: "jwt-svid", X5C: []string{base64.StdEncoding.EncodeToString(caDER)}},
			},
		}
		data, _ := json.Marshal(bundle)
		_, err := ParseTrustBundleToPEM(string(data))
		if err == nil {
			t.Fatal("expected error when only jwt-svid keys present")
		}
	})
}
