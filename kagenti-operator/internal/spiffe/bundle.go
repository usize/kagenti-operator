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
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"time"
)

// spiffeBundleJSON is the minimal structure of a SPIFFE trust bundle document.
type spiffeBundleJSON struct {
	Keys []spiffeBundleKey `json:"keys"`
}

type spiffeBundleKey struct {
	Use string   `json:"use"`
	X5C []string `json:"x5c"`
}

// ParseTrustBundleToPEM reads SPIFFE JSON bundle data and returns
// PEM-encoded CA certificates suitable for Envoy Gateway's caCertificateRefs.
func ParseTrustBundleToPEM(spiffeJSON string) ([]byte, error) {
	var bundle spiffeBundleJSON
	if err := json.Unmarshal([]byte(spiffeJSON), &bundle); err != nil {
		return nil, fmt.Errorf("failed to parse SPIFFE bundle JSON: %w", err)
	}

	var pemBytes []byte
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
			// Skip expired certificates. SPIRE bundles retain rotated-out
			// CAs that may have expired; Envoy rejects bundles containing them.
			if cert.NotAfter.Before(time.Now()) {
				continue
			}
			block := &pem.Block{
				Type:  "CERTIFICATE",
				Bytes: der,
			}
			pemBytes = append(pemBytes, pem.EncodeToMemory(block)...)
			count++
		}
	}

	if count == 0 {
		return nil, fmt.Errorf("SPIFFE bundle contains no x509-svid certificates")
	}

	return pemBytes, nil
}
