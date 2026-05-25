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

package v1alpha1

import (
	"context"
	"testing"

	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	gatewayv1alpha1 "github.com/kagenti/operator/api/gateway/v1alpha1"
)

func validAIGatewayForTest() *gatewayv1alpha1.AIGateway {
	return &gatewayv1alpha1.AIGateway{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-gw",
			Namespace: "default",
		},
		Spec: gatewayv1alpha1.AIGatewaySpec{
			GatewayClassName: "eg",
			Listeners: []gatewayv1alpha1.AIGatewayListener{
				{Name: "http", Port: 8080, Protocol: "HTTP"},
			},
			Providers: []gatewayv1alpha1.AIGatewayProvider{
				{
					Name:     "openai",
					Endpoint: "https://api.openai.com",
					Schema:   "OpenAI",
					CredentialRef: &gatewayv1alpha1.CredentialReference{
						Name: "openai-secret",
					},
					Models: []string{"gpt-4o"},
				},
			},
		},
	}
}

func TestRejectsEmptyGatewayClassName(t *testing.T) {
	g := NewWithT(t)
	v := &AIGatewayValidator{}
	aigw := validAIGatewayForTest()
	aigw.Spec.GatewayClassName = ""

	_, err := v.ValidateCreate(context.Background(), aigw)
	g.Expect(err).To(HaveOccurred())
	g.Expect(err.Error()).To(ContainSubstring("gatewayClassName is required"))
}

func TestRejectsEmptyListeners(t *testing.T) {
	g := NewWithT(t)
	v := &AIGatewayValidator{}
	aigw := validAIGatewayForTest()
	aigw.Spec.Listeners = nil

	_, err := v.ValidateCreate(context.Background(), aigw)
	g.Expect(err).To(HaveOccurred())
	g.Expect(err.Error()).To(ContainSubstring("at least one listener is required"))
}

func TestRejectsEmptyProviders(t *testing.T) {
	g := NewWithT(t)
	v := &AIGatewayValidator{}
	aigw := validAIGatewayForTest()
	aigw.Spec.Providers = nil

	_, err := v.ValidateCreate(context.Background(), aigw)
	g.Expect(err).To(HaveOccurred())
	g.Expect(err.Error()).To(ContainSubstring("at least one provider is required"))
}

func TestRejectsDuplicateProviderNames(t *testing.T) {
	g := NewWithT(t)
	v := &AIGatewayValidator{}
	aigw := validAIGatewayForTest()
	aigw.Spec.Providers = append(aigw.Spec.Providers, gatewayv1alpha1.AIGatewayProvider{
		Name:     "openai",
		Endpoint: "https://api.openai.com/v2",
		Schema:   "OpenAI",
		Models:   []string{"gpt-4o-mini"},
	})

	_, err := v.ValidateCreate(context.Background(), aigw)
	g.Expect(err).To(HaveOccurred())
	g.Expect(err.Error()).To(ContainSubstring("duplicate provider name"))
}

func TestRejectsEmptyEndpoint(t *testing.T) {
	g := NewWithT(t)
	v := &AIGatewayValidator{}
	aigw := validAIGatewayForTest()
	aigw.Spec.Providers[0].Endpoint = ""

	_, err := v.ValidateCreate(context.Background(), aigw)
	g.Expect(err).To(HaveOccurred())
	g.Expect(err.Error()).To(ContainSubstring("endpoint is required"))
}

func TestRejectsEmptyModels(t *testing.T) {
	g := NewWithT(t)
	v := &AIGatewayValidator{}
	aigw := validAIGatewayForTest()
	aigw.Spec.Providers[0].Models = nil

	_, err := v.ValidateCreate(context.Background(), aigw)
	g.Expect(err).To(HaveOccurred())
	g.Expect(err.Error()).To(ContainSubstring("at least one model is required"))
}

func TestRejectsInvalidSchema(t *testing.T) {
	g := NewWithT(t)
	v := &AIGatewayValidator{}
	aigw := validAIGatewayForTest()
	aigw.Spec.Providers[0].Schema = "InvalidSchema"

	_, err := v.ValidateCreate(context.Background(), aigw)
	g.Expect(err).To(HaveOccurred())
	g.Expect(err.Error()).To(ContainSubstring("invalid schema"))
}

func TestRejectsEmptyCredentialRefName(t *testing.T) {
	g := NewWithT(t)
	v := &AIGatewayValidator{}
	aigw := validAIGatewayForTest()
	aigw.Spec.Providers[0].CredentialRef = &gatewayv1alpha1.CredentialReference{Name: ""}

	_, err := v.ValidateCreate(context.Background(), aigw)
	g.Expect(err).To(HaveOccurred())
	g.Expect(err.Error()).To(ContainSubstring("credentialRef.name must be non-empty"))
}

func TestAcceptsValidAIGateway(t *testing.T) {
	g := NewWithT(t)
	v := &AIGatewayValidator{}
	aigw := validAIGatewayForTest()

	_, err := v.ValidateCreate(context.Background(), aigw)
	g.Expect(err).NotTo(HaveOccurred())
}

func TestAcceptsProviderWithoutCredentialRef(t *testing.T) {
	g := NewWithT(t)
	v := &AIGatewayValidator{}
	aigw := validAIGatewayForTest()
	aigw.Spec.Providers[0].CredentialRef = nil

	_, err := v.ValidateCreate(context.Background(), aigw)
	g.Expect(err).NotTo(HaveOccurred())
}

func TestValidateDeleteReturnsNil(t *testing.T) {
	g := NewWithT(t)
	v := &AIGatewayValidator{}
	aigw := validAIGatewayForTest()

	_, err := v.ValidateDelete(context.Background(), aigw)
	g.Expect(err).NotTo(HaveOccurred())
}

func TestValidateUpdateRejectsInvalidNewObject(t *testing.T) {
	g := NewWithT(t)
	v := &AIGatewayValidator{}
	oldObj := validAIGatewayForTest()
	newObj := validAIGatewayForTest()
	newObj.Spec.GatewayClassName = ""

	_, err := v.ValidateUpdate(context.Background(), oldObj, newObj)
	g.Expect(err).To(HaveOccurred())
	g.Expect(err.Error()).To(ContainSubstring("gatewayClassName is required"))
}

func TestValidateUpdateAcceptsValidChange(t *testing.T) {
	g := NewWithT(t)
	v := &AIGatewayValidator{}
	oldObj := validAIGatewayForTest()
	newObj := validAIGatewayForTest()
	newObj.Spec.Providers[0].Models = append(newObj.Spec.Providers[0].Models, "gpt-4o-mini")

	_, err := v.ValidateUpdate(context.Background(), oldObj, newObj)
	g.Expect(err).NotTo(HaveOccurred())
}

func TestAcceptsAllValidSchemas(t *testing.T) {
	schemas := []string{"OpenAI", "Anthropic", "AWSBedrock", "AzureOpenAI", "GoogleGenAI"}
	for _, schema := range schemas {
		t.Run(schema, func(t *testing.T) {
			g := NewWithT(t)
			v := &AIGatewayValidator{}
			aigw := validAIGatewayForTest()
			aigw.Spec.Providers[0].Schema = schema

			_, err := v.ValidateCreate(context.Background(), aigw)
			g.Expect(err).NotTo(HaveOccurred())
		})
	}
}

func TestCollectsMultipleValidationErrors(t *testing.T) {
	g := NewWithT(t)
	v := &AIGatewayValidator{}
	aigw := validAIGatewayForTest()
	aigw.Spec.GatewayClassName = ""
	aigw.Spec.Listeners = nil
	aigw.Spec.Providers = nil

	_, err := v.ValidateCreate(context.Background(), aigw)
	g.Expect(err).To(HaveOccurred())
	g.Expect(err.Error()).To(ContainSubstring("gatewayClassName is required"))
	g.Expect(err.Error()).To(ContainSubstring("at least one listener is required"))
	g.Expect(err.Error()).To(ContainSubstring("at least one provider is required"))
}

func TestCreateRejectsWrongType(t *testing.T) {
	g := NewWithT(t)
	v := &AIGatewayValidator{}

	// Pass a non-AIGateway object.
	_, err := v.ValidateCreate(context.Background(), &corev1.Pod{})
	g.Expect(err).To(HaveOccurred())
	g.Expect(err.Error()).To(ContainSubstring("expected an AIGateway"))
}

func TestUpdateRejectsWrongType(t *testing.T) {
	g := NewWithT(t)
	v := &AIGatewayValidator{}

	aigw := validAIGatewayForTest()
	_, err := v.ValidateUpdate(context.Background(), aigw, &corev1.Pod{})
	g.Expect(err).To(HaveOccurred())
	g.Expect(err.Error()).To(ContainSubstring("expected an AIGateway"))
}

func TestDeleteRejectsWrongType(t *testing.T) {
	g := NewWithT(t)
	v := &AIGatewayValidator{}

	_, err := v.ValidateDelete(context.Background(), &corev1.Pod{})
	g.Expect(err).To(HaveOccurred())
	g.Expect(err.Error()).To(ContainSubstring("expected an AIGateway"))
}

func TestAcceptsValidMTLSConfig(t *testing.T) {
	g := NewWithT(t)
	v := &AIGatewayValidator{}
	aigw := validAIGatewayForTest()
	aigw.Spec.MTLS = &gatewayv1alpha1.AIGatewayMTLS{
		TrustDomain: "example.org",
		TrustBundleConfigMap: gatewayv1alpha1.TrustBundleRef{
			Name:      "spire-bundle",
			Namespace: "spire-system",
		},
	}

	_, err := v.ValidateCreate(context.Background(), aigw)
	g.Expect(err).NotTo(HaveOccurred())
}

func TestRejectsEmptyMTLSTrustDomain(t *testing.T) {
	g := NewWithT(t)
	v := &AIGatewayValidator{}
	aigw := validAIGatewayForTest()
	aigw.Spec.MTLS = &gatewayv1alpha1.AIGatewayMTLS{
		TrustDomain: "",
		TrustBundleConfigMap: gatewayv1alpha1.TrustBundleRef{
			Name:      "spire-bundle",
			Namespace: "spire-system",
		},
	}

	_, err := v.ValidateCreate(context.Background(), aigw)
	g.Expect(err).To(HaveOccurred())
	g.Expect(err.Error()).To(ContainSubstring("trustDomain is required"))
}

func TestRejectsEmptyMTLSTrustBundleConfigMapName(t *testing.T) {
	g := NewWithT(t)
	v := &AIGatewayValidator{}
	aigw := validAIGatewayForTest()
	aigw.Spec.MTLS = &gatewayv1alpha1.AIGatewayMTLS{
		TrustDomain: "example.org",
		TrustBundleConfigMap: gatewayv1alpha1.TrustBundleRef{
			Name:      "",
			Namespace: "spire-system",
		},
	}

	_, err := v.ValidateCreate(context.Background(), aigw)
	g.Expect(err).To(HaveOccurred())
	g.Expect(err.Error()).To(ContainSubstring("trustBundleConfigMap.name is required"))
}

func TestRejectsEmptyMTLSTrustBundleConfigMapNamespace(t *testing.T) {
	g := NewWithT(t)
	v := &AIGatewayValidator{}
	aigw := validAIGatewayForTest()
	aigw.Spec.MTLS = &gatewayv1alpha1.AIGatewayMTLS{
		TrustDomain: "example.org",
		TrustBundleConfigMap: gatewayv1alpha1.TrustBundleRef{
			Name:      "spire-bundle",
			Namespace: "",
		},
	}

	_, err := v.ValidateCreate(context.Background(), aigw)
	g.Expect(err).To(HaveOccurred())
	g.Expect(err.Error()).To(ContainSubstring("trustBundleConfigMap.namespace is required"))
}

func TestRejectsEmptyMTLSServerCertRefName(t *testing.T) {
	g := NewWithT(t)
	v := &AIGatewayValidator{}
	aigw := validAIGatewayForTest()
	aigw.Spec.MTLS = &gatewayv1alpha1.AIGatewayMTLS{
		TrustDomain: "example.org",
		TrustBundleConfigMap: gatewayv1alpha1.TrustBundleRef{
			Name:      "spire-bundle",
			Namespace: "spire-system",
		},
		ServerCertRef: &gatewayv1alpha1.CertificateReference{Name: ""},
	}

	_, err := v.ValidateCreate(context.Background(), aigw)
	g.Expect(err).To(HaveOccurred())
	g.Expect(err.Error()).To(ContainSubstring("serverCertRef.name must be non-empty"))
}

func TestAcceptsMTLSWithServerCertRef(t *testing.T) {
	g := NewWithT(t)
	v := &AIGatewayValidator{}
	aigw := validAIGatewayForTest()
	aigw.Spec.MTLS = &gatewayv1alpha1.AIGatewayMTLS{
		TrustDomain: "example.org",
		TrustBundleConfigMap: gatewayv1alpha1.TrustBundleRef{
			Name:      "spire-bundle",
			Namespace: "spire-system",
		},
		ServerCertRef: &gatewayv1alpha1.CertificateReference{Name: "my-tls-cert"},
	}

	_, err := v.ValidateCreate(context.Background(), aigw)
	g.Expect(err).NotTo(HaveOccurred())
}

func TestAcceptsNilMTLS(t *testing.T) {
	g := NewWithT(t)
	v := &AIGatewayValidator{}
	aigw := validAIGatewayForTest()
	aigw.Spec.MTLS = nil

	_, err := v.ValidateCreate(context.Background(), aigw)
	g.Expect(err).NotTo(HaveOccurred())
}
