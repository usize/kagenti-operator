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

func generateTestCert(t *testing.T, cn string) (certDER []byte, certPEM string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generating key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: cn},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("creating cert: %v", err)
	}
	pemBlock := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	return der, string(pemBlock)
}

func TestConvertBundleData_SPIFFEJson(t *testing.T) {
	der1, _ := generateTestCert(t, "test-ca-1")
	der2, _ := generateTestCert(t, "test-ca-2")

	bundle := spiffeBundleJSON{
		Keys: []spiffeBundleKey{
			{
				Use: "x509-svid",
				X5C: []string{base64.StdEncoding.EncodeToString(der1)},
			},
			{
				Use: "jwt-svid", // should be skipped
				X5C: []string{base64.StdEncoding.EncodeToString(der1)},
			},
			{
				Use: "x509-svid",
				X5C: []string{base64.StdEncoding.EncodeToString(der2)},
			},
		},
	}
	raw, err := json.Marshal(bundle)
	if err != nil {
		t.Fatalf("marshaling bundle: %v", err)
	}

	pemBytes, err := ConvertBundleData(string(raw))
	if err != nil {
		t.Fatalf("ConvertBundleData failed: %v", err)
	}

	// Should contain exactly 2 PEM certificates
	count := strings.Count(string(pemBytes), "-----BEGIN CERTIFICATE-----")
	if count != 2 {
		t.Errorf("expected 2 PEM certificates, got %d", count)
	}

	// Verify each can be parsed
	rest := pemBytes
	for i := 0; i < 2; i++ {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			t.Fatalf("failed to decode PEM block %d", i)
		}
		if _, err := x509.ParseCertificate(block.Bytes); err != nil {
			t.Errorf("failed to parse certificate %d: %v", i, err)
		}
	}
}

func TestConvertBundleData_PEMPassthrough(t *testing.T) {
	_, pem1 := generateTestCert(t, "test-ca-1")
	_, pem2 := generateTestCert(t, "test-ca-2")

	raw := pem1 + pem2

	pemBytes, err := ConvertBundleData(raw)
	if err != nil {
		t.Fatalf("ConvertBundleData failed: %v", err)
	}

	count := strings.Count(string(pemBytes), "-----BEGIN CERTIFICATE-----")
	if count != 2 {
		t.Errorf("expected 2 PEM certificates, got %d", count)
	}
}

func TestConvertBundleData_EmptyInput(t *testing.T) {
	_, err := ConvertBundleData("")
	if err == nil {
		t.Error("expected error for empty input")
	}
}

func TestConvertBundleData_WhitespaceOnly(t *testing.T) {
	_, err := ConvertBundleData("   \n  ")
	if err == nil {
		t.Error("expected error for whitespace-only input")
	}
}

func TestConvertBundleData_InvalidJSON(t *testing.T) {
	_, err := ConvertBundleData("{invalid json}")
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestConvertBundleData_NoCerts(t *testing.T) {
	bundle := spiffeBundleJSON{
		Keys: []spiffeBundleKey{
			{Use: "jwt-svid", X5C: []string{"dGVzdA=="}},
		},
	}
	raw, _ := json.Marshal(bundle)

	_, err := ConvertBundleData(string(raw))
	if err == nil {
		t.Error("expected error when no x509-svid certs found")
	}
}

func TestConvertBundleData_InvalidBase64(t *testing.T) {
	bundle := spiffeBundleJSON{
		Keys: []spiffeBundleKey{
			{Use: "x509-svid", X5C: []string{"!!!not-base64!!!"}},
		},
	}
	raw, _ := json.Marshal(bundle)

	_, err := ConvertBundleData(string(raw))
	if err == nil {
		t.Error("expected error for invalid base64")
	}
}

func TestConvertBundleData_InvalidCertDER(t *testing.T) {
	bundle := spiffeBundleJSON{
		Keys: []spiffeBundleKey{
			{Use: "x509-svid", X5C: []string{base64.StdEncoding.EncodeToString([]byte("not a cert"))}},
		},
	}
	raw, _ := json.Marshal(bundle)

	_, err := ConvertBundleData(string(raw))
	if err == nil {
		t.Error("expected error for invalid certificate DER")
	}
}
