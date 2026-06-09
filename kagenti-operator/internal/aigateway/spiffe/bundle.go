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

// Package spiffe converts SPIFFE trust bundles to PEM format for use in
// Kubernetes Secrets. It follows the parsing patterns in
// internal/signature/x5c.go but outputs PEM bytes.
package spiffe

import (
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"strings"
)

// spiffeBundleJSON is the minimal structure of a SPIFFE trust bundle document.
type spiffeBundleJSON struct {
	Keys []spiffeBundleKey `json:"keys"`
}

type spiffeBundleKey struct {
	Use string   `json:"use"`
	X5C []string `json:"x5c"`
}

// ConvertBundleData converts raw trust bundle data to PEM-encoded certificates.
// It auto-detects the format:
//   - SPIFFE JSON bundle (JWK set with x509-svid keys) → parsed and converted to PEM
//   - PEM data → passed through as-is
//
// Returns an error if the data is empty, contains no certificates, or is malformed.
func ConvertBundleData(raw string) ([]byte, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("empty trust bundle data")
	}

	// Auto-detect PEM format
	if strings.Contains(raw, "-----BEGIN CERTIFICATE-----") {
		pemBytes, count := validatePEM(raw)
		if count == 0 {
			return nil, fmt.Errorf("PEM data contains no valid certificates")
		}
		return pemBytes, nil
	}

	// Parse as SPIFFE JSON bundle
	return parseSPIFFEJSON(raw)
}

// validatePEM parses PEM data, validates each certificate, and returns clean PEM output.
func validatePEM(raw string) ([]byte, int) {
	var result []byte
	count := 0
	rest := []byte(raw)
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			continue
		}
		if _, err := x509.ParseCertificate(block.Bytes); err != nil {
			continue
		}
		result = append(result, pem.EncodeToMemory(block)...)
		count++
	}
	return result, count
}

// parseSPIFFEJSON parses a SPIFFE trust bundle document (JSON with JWK set)
// and extracts x509-svid certificates as PEM bytes.
func parseSPIFFEJSON(raw string) ([]byte, error) {
	var bundle spiffeBundleJSON
	if err := json.Unmarshal([]byte(raw), &bundle); err != nil {
		return nil, fmt.Errorf("failed to parse SPIFFE bundle JSON: %w", err)
	}

	var result []byte
	count := 0
	for _, k := range bundle.Keys {
		if k.Use != "x509-svid" {
			continue
		}
		for _, b64 := range k.X5C {
			der, err := base64.StdEncoding.DecodeString(b64)
			if err != nil {
				return nil, fmt.Errorf("failed to decode x5c cert from SPIFFE bundle: %w", err)
			}
			cert, err := x509.ParseCertificate(der)
			if err != nil {
				return nil, fmt.Errorf("failed to parse certificate from SPIFFE bundle: %w", err)
			}
			block := &pem.Block{
				Type:  "CERTIFICATE",
				Bytes: cert.Raw,
			}
			result = append(result, pem.EncodeToMemory(block)...)
			count++
		}
	}

	if count == 0 {
		return nil, fmt.Errorf("SPIFFE bundle contains no x509-svid certificates")
	}

	return result, nil
}
