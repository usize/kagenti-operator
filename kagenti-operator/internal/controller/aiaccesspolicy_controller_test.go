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

package controller

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	aigwv1a1 "github.com/envoyproxy/ai-gateway/api/v1alpha1"
	egv1a1 "github.com/envoyproxy/gateway/api/v1alpha1"

	aigatewayv1alpha1 "github.com/kagenti/operator/api/aigateway/v1alpha1"
)

func newAIAccessTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{
		corev1.AddToScheme,
		aigatewayv1alpha1.AddToScheme,
		egv1a1.AddToScheme,
		aigwv1a1.AddToScheme,
	} {
		if err := add(s); err != nil {
			t.Fatalf("adding scheme: %v", err)
		}
	}
	return s
}

func generateTestCACert(t *testing.T) (certDER []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generating key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test-ca"},
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
	return der
}

type spiffeBundleKey struct {
	Use string   `json:"use"`
	X5C []string `json:"x5c"`
}

type spiffeBundleJSON struct {
	Keys []spiffeBundleKey `json:"keys"`
}

func TestAIAccessPolicy_MissingConfigMap(t *testing.T) {
	scheme := newAIAccessTestScheme(t)

	policy := &aigatewayv1alpha1.AIAccessPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-access",
			Namespace: "team1",
		},
		Spec: aigatewayv1alpha1.AIAccessPolicySpec{
			TargetRef: aigatewayv1alpha1.TargetRef{
				Group: "gateway.networking.k8s.io",
				Kind:  "Gateway",
				Name:  "ai-gateway",
			},
			MTLS: aigatewayv1alpha1.MTLSSpec{
				TrustDomain: "localtest.me",
				TrustBundleConfigMap: aigatewayv1alpha1.ConfigMapRef{
					Name:      "spire-bundle",
					Namespace: "spire-system",
					Key:       "bundle.spiffe",
				},
			},
		},
	}

	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "team1"}}
	nsSpire := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "spire-system"}}

	cb := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(policy, ns, nsSpire).
		WithStatusSubresource(policy)
	r := &AIAccessPolicyReconciler{
		Client:   cb.Build(),
		Scheme:   scheme,
		Recorder: record.NewFakeRecorder(10),
	}

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "test-access", Namespace: "team1"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should requeue to retry
	if result.RequeueAfter == 0 {
		t.Error("expected requeue when ConfigMap is missing")
	}
}

func TestAIAccessPolicy_ValidBundle(t *testing.T) {
	scheme := newAIAccessTestScheme(t)

	certDER := generateTestCACert(t)
	bundle := spiffeBundleJSON{
		Keys: []spiffeBundleKey{
			{
				Use: "x509-svid",
				X5C: []string{base64.StdEncoding.EncodeToString(certDER)},
			},
		},
	}
	bundleJSON, _ := json.Marshal(bundle)

	policy := &aigatewayv1alpha1.AIAccessPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-access",
			Namespace: "team1",
		},
		Spec: aigatewayv1alpha1.AIAccessPolicySpec{
			TargetRef: aigatewayv1alpha1.TargetRef{
				Group: "gateway.networking.k8s.io",
				Kind:  "Gateway",
				Name:  "ai-gateway",
			},
			MTLS: aigatewayv1alpha1.MTLSSpec{
				TrustDomain: "localtest.me",
				TrustBundleConfigMap: aigatewayv1alpha1.ConfigMapRef{
					Name:      "spire-bundle",
					Namespace: "spire-system",
					Key:       "bundle.spiffe",
				},
			},
		},
	}

	bundleCM := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "spire-bundle",
			Namespace: "spire-system",
		},
		Data: map[string]string{
			"bundle.spiffe": string(bundleJSON),
		},
	}

	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "team1"}}
	nsSpire := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "spire-system"}}

	cb := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(policy, bundleCM, ns, nsSpire).
		WithStatusSubresource(policy)
	r := &AIAccessPolicyReconciler{
		Client:   cb.Build(),
		Scheme:   scheme,
		Recorder: record.NewFakeRecorder(10),
	}

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "test-access", Namespace: "team1"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should requeue for bundle refresh
	if result.RequeueAfter != bundleRefreshInterval {
		t.Errorf("expected requeue after %v, got %v", bundleRefreshInterval, result.RequeueAfter)
	}

	// Verify CA Secret was created
	caSecret := &corev1.Secret{}
	if err := r.Get(context.Background(), types.NamespacedName{
		Name: "test-access-mtls-ca", Namespace: "team1",
	}, caSecret); err != nil {
		t.Fatalf("CA secret not created: %v", err)
	}
	if _, ok := caSecret.Data["ca.crt"]; !ok {
		t.Error("CA secret missing ca.crt key")
	}

	// Verify CA cert is valid PEM
	block, _ := pem.Decode(caSecret.Data["ca.crt"])
	if block == nil {
		t.Fatal("CA cert is not valid PEM")
	}

	// Verify server cert Secret was created (self-signed since no serverCertRef)
	serverSecret := &corev1.Secret{}
	if err := r.Get(context.Background(), types.NamespacedName{
		Name: "test-access-mtls-server", Namespace: "team1",
	}, serverSecret); err != nil {
		t.Fatalf("server cert secret not created: %v", err)
	}
	if serverSecret.Type != corev1.SecretTypeTLS {
		t.Errorf("expected TLS secret type, got %q", serverSecret.Type)
	}
}

func TestAIAccessPolicy_PEMBundle(t *testing.T) {
	scheme := newAIAccessTestScheme(t)

	certDER := generateTestCACert(t)
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})

	policy := &aigatewayv1alpha1.AIAccessPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-access",
			Namespace: "team1",
		},
		Spec: aigatewayv1alpha1.AIAccessPolicySpec{
			TargetRef: aigatewayv1alpha1.TargetRef{
				Group: "gateway.networking.k8s.io",
				Kind:  "Gateway",
				Name:  "ai-gateway",
			},
			MTLS: aigatewayv1alpha1.MTLSSpec{
				TrustDomain: "localtest.me",
				TrustBundleConfigMap: aigatewayv1alpha1.ConfigMapRef{
					Name:      "spire-bundle",
					Namespace: "team1",
					Key:       "bundle.crt",
				},
			},
		},
	}

	bundleCM := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "spire-bundle",
			Namespace: "team1",
		},
		Data: map[string]string{
			"bundle.crt": string(certPEM),
		},
	}

	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "team1"}}

	cb := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(policy, bundleCM, ns).
		WithStatusSubresource(policy)
	r := &AIAccessPolicyReconciler{
		Client:   cb.Build(),
		Scheme:   scheme,
		Recorder: record.NewFakeRecorder(10),
	}

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "test-access", Namespace: "team1"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify CA Secret was created
	caSecret := &corev1.Secret{}
	if err := r.Get(context.Background(), types.NamespacedName{
		Name: "test-access-mtls-ca", Namespace: "team1",
	}, caSecret); err != nil {
		t.Fatalf("CA secret not created: %v", err)
	}
}
