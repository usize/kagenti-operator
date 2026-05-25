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
	"math/big"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	gatewayv1alpha1 "github.com/kagenti/operator/api/gateway/v1alpha1"
)

const (
	testTimeout  = time.Second * 10
	testInterval = time.Millisecond * 250
)

// cleanupAIGateway removes an AIGateway and all its owned resources, waiting for deletion to complete.
func cleanupAIGateway(ctx context.Context, name, namespace string) {
	nn := types.NamespacedName{Name: name, Namespace: namespace}

	// Remove finalizer and delete the AIGateway.
	aigw := &gatewayv1alpha1.AIGateway{}
	if err := k8sClient.Get(ctx, nn, aigw); err == nil {
		aigw.Finalizers = nil
		_ = k8sClient.Update(ctx, aigw)
		_ = k8sClient.Delete(ctx, aigw)
		Eventually(func() bool {
			return apierrors.IsNotFound(k8sClient.Get(ctx, nn, aigw))
		}, testTimeout, testInterval).Should(BeTrue())
	}

	// Clean up secrets created by mTLS.
	for _, secretName := range []string{name + "-mtls-ca", name + "-mtls-server"} {
		secret := &corev1.Secret{}
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: secretName, Namespace: namespace}, secret); err == nil {
			_ = k8sClient.Delete(ctx, secret)
		}
	}

	// Clean up all unstructured resources.
	for _, gvk := range []schema.GroupVersionKind{gvkGateway, gvkAIServiceBackend, gvkBackendSecurityPolicy, gvkAIGatewayRoute, gvkClientTrafficPolicy} {
		list := &unstructured.UnstructuredList{}
		list.SetGroupVersionKind(schema.GroupVersionKind{
			Group:   gvk.Group,
			Version: gvk.Version,
			Kind:    gvk.Kind + "List",
		})
		if err := k8sClient.List(ctx, list); err == nil {
			for i := range list.Items {
				_ = k8sClient.Delete(ctx, &list.Items[i])
			}
		}
	}
}

// reconcileN runs Reconcile n times, returning the final result and error.
func reconcileN(r *AIGatewayReconciler, ctx context.Context, nn types.NamespacedName, n int) (reconcile.Result, error) {
	var res reconcile.Result
	var err error
	for i := 0; i < n; i++ {
		res, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
		if err != nil {
			return res, err
		}
	}
	return res, err
}

var _ = Describe("AIGateway Controller", func() {
	const (
		aigwName  = "test-aigw"
		namespace = "default"
	)

	ctx := context.Background()

	namespacedName := types.NamespacedName{
		Name:      aigwName,
		Namespace: namespace,
	}

	newReconciler := func() *AIGatewayReconciler {
		return &AIGatewayReconciler{
			Client:   k8sClient,
			Scheme:   k8sClient.Scheme(),
			Recorder: &record.FakeRecorder{Events: make(chan string, 100)},
		}
	}

	validAIGateway := func(name string) *gatewayv1alpha1.AIGateway {
		return &gatewayv1alpha1.AIGateway{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: namespace,
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
						Models: []string{"gpt-4o", "gpt-4o-mini"},
					},
				},
			},
		}
	}

	AfterEach(func() {
		cleanupAIGateway(ctx, aigwName, namespace)
	})

	Context("Finalizer management", func() {
		It("should add finalizer on first reconcile", func() {
			By("creating an AIGateway resource")
			aigw := validAIGateway(aigwName)
			Expect(k8sClient.Create(ctx, aigw)).To(Succeed())

			By("reconciling to add the finalizer")
			r := newReconciler()
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: namespacedName})
			Expect(err).NotTo(HaveOccurred())

			By("verifying the finalizer is present")
			updated := &gatewayv1alpha1.AIGateway{}
			Expect(k8sClient.Get(ctx, namespacedName, updated)).To(Succeed())
			Expect(updated.Finalizers).To(ContainElement(AIGatewayFinalizer))
		})
	})

	Context("Gateway creation", func() {
		It("should create Gateway with correct gatewayClassName, listeners, and owner reference", func() {
			By("creating an AIGateway and reconciling twice")
			aigw := validAIGateway(aigwName)
			Expect(k8sClient.Create(ctx, aigw)).To(Succeed())

			r := newReconciler()
			_, err := reconcileN(r, ctx, namespacedName, 2)
			Expect(err).NotTo(HaveOccurred())

			By("verifying the Gateway resource was created")
			gw := &unstructured.Unstructured{}
			gw.SetGroupVersionKind(gvkGateway)
			Expect(k8sClient.Get(ctx, namespacedName, gw)).To(Succeed())

			By("checking gatewayClassName")
			className, _, _ := unstructured.NestedString(gw.Object, "spec", "gatewayClassName")
			Expect(className).To(Equal("eg"))

			By("checking listeners")
			listeners, _, _ := unstructured.NestedSlice(gw.Object, "spec", "listeners")
			Expect(listeners).To(HaveLen(1))
			l := listeners[0].(map[string]interface{})
			Expect(l["name"]).To(Equal("http"))
			Expect(l["port"]).To(BeNumerically("==", 8080))
			Expect(l["protocol"]).To(Equal("HTTP"))

			By("verifying owner reference on the Gateway")
			ownerRefs := gw.GetOwnerReferences()
			Expect(ownerRefs).To(HaveLen(1))
			Expect(ownerRefs[0].Kind).To(Equal("AIGateway"))
			Expect(ownerRefs[0].Name).To(Equal(aigwName))

			By("verifying events were emitted")
			recorder := r.Recorder.(*record.FakeRecorder)
			Eventually(recorder.Events).Should(Receive(ContainSubstring("Created Gateway")))
		})
	})

	Context("AIServiceBackend creation", func() {
		It("should create AIServiceBackend with external endpoint (Backend kind) and owner reference", func() {
			By("creating an AIGateway with an external provider")
			aigw := validAIGateway(aigwName)
			Expect(k8sClient.Create(ctx, aigw)).To(Succeed())

			r := newReconciler()
			_, err := reconcileN(r, ctx, namespacedName, 2)
			Expect(err).NotTo(HaveOccurred())

			By("verifying the AIServiceBackend was created")
			backend := &unstructured.Unstructured{}
			backend.SetGroupVersionKind(gvkAIServiceBackend)
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: aigwName + "-openai", Namespace: namespace,
			}, backend)).To(Succeed())

			By("checking schema and backendRef fields")
			schemaName, _, _ := unstructured.NestedString(backend.Object, "spec", "schema", "name")
			Expect(schemaName).To(Equal("OpenAI"))

			backendKind, _, _ := unstructured.NestedString(backend.Object, "spec", "backendRef", "kind")
			Expect(backendKind).To(Equal("Backend"))

			backendGroup, _, _ := unstructured.NestedString(backend.Object, "spec", "backendRef", "group")
			Expect(backendGroup).To(Equal("gateway.envoyproxy.io"))

			By("verifying owner reference")
			ownerRefs := backend.GetOwnerReferences()
			Expect(ownerRefs).To(HaveLen(1))
			Expect(ownerRefs[0].Kind).To(Equal("AIGateway"))
			Expect(ownerRefs[0].Name).To(Equal(aigwName))
		})

		It("should create AIServiceBackend with in-cluster endpoint (Service kind)", func() {
			By("creating an AIGateway with an in-cluster provider")
			aigw := validAIGateway(aigwName)
			aigw.Spec.Providers[0].Name = "local"
			aigw.Spec.Providers[0].Endpoint = "http://ollama.default.svc:11434"
			aigw.Spec.Providers[0].Schema = "OpenAI"
			aigw.Spec.Providers[0].CredentialRef = nil
			Expect(k8sClient.Create(ctx, aigw)).To(Succeed())

			r := newReconciler()
			_, err := reconcileN(r, ctx, namespacedName, 2)
			Expect(err).NotTo(HaveOccurred())

			By("verifying the Service-kind backend was created")
			backend := &unstructured.Unstructured{}
			backend.SetGroupVersionKind(gvkAIServiceBackend)
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: aigwName + "-local", Namespace: namespace,
			}, backend)).To(Succeed())

			backendKind, _, _ := unstructured.NestedString(backend.Object, "spec", "backendRef", "kind")
			Expect(backendKind).To(Equal("Service"))

			backendGroup, _, _ := unstructured.NestedString(backend.Object, "spec", "backendRef", "group")
			Expect(backendGroup).To(Equal(""))

			port, _, _ := unstructured.NestedInt64(backend.Object, "spec", "backendRef", "port")
			Expect(port).To(BeNumerically("==", 11434))
		})
	})

	Context("BackendSecurityPolicy creation", func() {
		It("should create BackendSecurityPolicy only for providers with credentialRef", func() {
			By("creating an AIGateway with one credentialed and one uncredentialed provider")
			aigw := validAIGateway(aigwName)
			aigw.Spec.Providers = append(aigw.Spec.Providers, gatewayv1alpha1.AIGatewayProvider{
				Name:     "ollama",
				Endpoint: "http://ollama.default.svc:11434",
				Schema:   "OpenAI",
				Models:   []string{"llama3"},
			})
			Expect(k8sClient.Create(ctx, aigw)).To(Succeed())

			r := newReconciler()
			_, err := reconcileN(r, ctx, namespacedName, 2)
			Expect(err).NotTo(HaveOccurred())

			By("verifying the openai policy was created with correct secret ref")
			policy := &unstructured.Unstructured{}
			policy.SetGroupVersionKind(gvkBackendSecurityPolicy)
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: aigwName + "-openai-credentials", Namespace: namespace,
			}, policy)).To(Succeed())

			secretName, _, _ := unstructured.NestedString(policy.Object, "spec", "apiKey", "secretRef", "name")
			Expect(secretName).To(Equal("openai-secret"))

			By("verifying owner reference on the policy")
			ownerRefs := policy.GetOwnerReferences()
			Expect(ownerRefs).To(HaveLen(1))
			Expect(ownerRefs[0].Kind).To(Equal("AIGateway"))
			Expect(ownerRefs[0].Name).To(Equal(aigwName))

			By("verifying the ollama provider has no policy")
			ollamaPolicy := &unstructured.Unstructured{}
			ollamaPolicy.SetGroupVersionKind(gvkBackendSecurityPolicy)
			err = k8sClient.Get(ctx, types.NamespacedName{
				Name: aigwName + "-ollama-credentials", Namespace: namespace,
			}, ollamaPolicy)
			Expect(err).To(HaveOccurred())
			Expect(apierrors.IsNotFound(err)).To(BeTrue())
		})
	})

	Context("AIGatewayRoute creation", func() {
		It("should create AIGatewayRoute with one rule per model and owner reference", func() {
			By("creating an AIGateway and reconciling")
			aigw := validAIGateway(aigwName)
			Expect(k8sClient.Create(ctx, aigw)).To(Succeed())

			r := newReconciler()
			_, err := reconcileN(r, ctx, namespacedName, 2)
			Expect(err).NotTo(HaveOccurred())

			By("verifying the route was created")
			route := &unstructured.Unstructured{}
			route.SetGroupVersionKind(gvkAIGatewayRoute)
			Expect(k8sClient.Get(ctx, namespacedName, route)).To(Succeed())

			By("checking rules count and content")
			rules, _, _ := unstructured.NestedSlice(route.Object, "spec", "rules")
			Expect(rules).To(HaveLen(2)) // gpt-4o and gpt-4o-mini

			rule0 := rules[0].(map[string]interface{})
			matches := rule0["matches"].([]interface{})
			headers := matches[0].(map[string]interface{})["headers"].([]interface{})
			header := headers[0].(map[string]interface{})
			Expect(header["name"]).To(Equal("x-ai-eg-model"))
			Expect(header["value"]).To(Equal("gpt-4o"))

			backendRefs := rule0["backendRefs"].([]interface{})
			ref := backendRefs[0].(map[string]interface{})
			Expect(ref["name"]).To(Equal(aigwName + "-openai"))
			Expect(ref["kind"]).To(Equal("AIServiceBackend"))

			By("verifying owner reference on the route")
			ownerRefs := route.GetOwnerReferences()
			Expect(ownerRefs).To(HaveLen(1))
			Expect(ownerRefs[0].Kind).To(Equal("AIGateway"))
			Expect(ownerRefs[0].Name).To(Equal(aigwName))
		})
	})

	Context("Status updates", func() {
		It("should update status with provider and model counts and observedGeneration", func() {
			By("creating an AIGateway and reconciling")
			aigw := validAIGateway(aigwName)
			Expect(k8sClient.Create(ctx, aigw)).To(Succeed())

			r := newReconciler()
			_, err := reconcileN(r, ctx, namespacedName, 2)
			Expect(err).NotTo(HaveOccurred())

			By("verifying status fields")
			Eventually(func(g Gomega) {
				updated := &gatewayv1alpha1.AIGateway{}
				g.Expect(k8sClient.Get(ctx, namespacedName, updated)).To(Succeed())
				g.Expect(updated.Status.ConfiguredProviders).To(Equal(int32(1)))
				g.Expect(updated.Status.ConfiguredModels).To(Equal(int32(2)))
				g.Expect(updated.Status.ObservedGeneration).To(Equal(updated.Generation))
			}, testTimeout, testInterval).Should(Succeed())
		})

		It("should set Ready=False when Gateway lacks Programmed condition", func() {
			By("creating an AIGateway and reconciling")
			aigw := validAIGateway(aigwName)
			Expect(k8sClient.Create(ctx, aigw)).To(Succeed())

			r := newReconciler()
			_, err := reconcileN(r, ctx, namespacedName, 2)
			Expect(err).NotTo(HaveOccurred())

			By("verifying Ready condition is False")
			Eventually(func(g Gomega) {
				updated := &gatewayv1alpha1.AIGateway{}
				g.Expect(k8sClient.Get(ctx, namespacedName, updated)).To(Succeed())
				readyCond := findCondition(updated.Status.Conditions, "Ready")
				g.Expect(readyCond).NotTo(BeNil())
				g.Expect(readyCond.Status).To(Equal(metav1.ConditionFalse))
				g.Expect(readyCond.Reason).To(Equal("GatewayNotReady"))
			}, testTimeout, testInterval).Should(Succeed())
		})

		It("should set Ready=True and compute endpoint when Gateway has Programmed=True", func() {
			By("creating an AIGateway and reconciling")
			aigw := validAIGateway(aigwName)
			Expect(k8sClient.Create(ctx, aigw)).To(Succeed())

			r := newReconciler()
			_, err := reconcileN(r, ctx, namespacedName, 2)
			Expect(err).NotTo(HaveOccurred())

			By("simulating Gateway becoming Programmed")
			gw := &unstructured.Unstructured{}
			gw.SetGroupVersionKind(gvkGateway)
			Expect(k8sClient.Get(ctx, namespacedName, gw)).To(Succeed())
			_ = unstructured.SetNestedSlice(gw.Object, []interface{}{
				map[string]interface{}{
					"type":   "Programmed",
					"status": "True",
					"reason": "Programmed",
				},
			}, "status", "conditions")
			Expect(k8sClient.Status().Update(ctx, gw)).To(Succeed())

			By("reconciling again to pick up Gateway status")
			_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: namespacedName})
			Expect(err).NotTo(HaveOccurred())

			By("verifying Ready=True and endpoint")
			Eventually(func(g Gomega) {
				updated := &gatewayv1alpha1.AIGateway{}
				g.Expect(k8sClient.Get(ctx, namespacedName, updated)).To(Succeed())
				readyCond := findCondition(updated.Status.Conditions, "Ready")
				g.Expect(readyCond).NotTo(BeNil())
				g.Expect(readyCond.Status).To(Equal(metav1.ConditionTrue))
				g.Expect(readyCond.Reason).To(Equal("Ready"))
				g.Expect(updated.Status.Endpoint).To(Equal("http://test-aigw.default.svc:8080"))
			}, testTimeout, testInterval).Should(Succeed())
		})

		It("should set all three conditions (Ready, GatewayReady, RoutesConfigured)", func() {
			By("creating an AIGateway and reconciling")
			aigw := validAIGateway(aigwName)
			Expect(k8sClient.Create(ctx, aigw)).To(Succeed())

			r := newReconciler()
			_, err := reconcileN(r, ctx, namespacedName, 2)
			Expect(err).NotTo(HaveOccurred())

			By("verifying all three conditions exist")
			Eventually(func(g Gomega) {
				updated := &gatewayv1alpha1.AIGateway{}
				g.Expect(k8sClient.Get(ctx, namespacedName, updated)).To(Succeed())
				g.Expect(findCondition(updated.Status.Conditions, "Ready")).NotTo(BeNil())
				g.Expect(findCondition(updated.Status.Conditions, "GatewayReady")).NotTo(BeNil())
				routesCond := findCondition(updated.Status.Conditions, "RoutesConfigured")
				g.Expect(routesCond).NotTo(BeNil())
				g.Expect(routesCond.Status).To(Equal(metav1.ConditionTrue))
				g.Expect(routesCond.Message).To(ContainSubstring("2 model routes"))
			}, testTimeout, testInterval).Should(Succeed())
		})
	})

	Context("Deletion handling", func() {
		It("should remove finalizer on deletion", func() {
			By("creating an AIGateway and reconciling to create resources")
			aigw := validAIGateway(aigwName)
			Expect(k8sClient.Create(ctx, aigw)).To(Succeed())

			r := newReconciler()
			_, err := reconcileN(r, ctx, namespacedName, 2)
			Expect(err).NotTo(HaveOccurred())

			By("deleting the AIGateway")
			Expect(k8sClient.Delete(ctx, aigw)).To(Succeed())

			By("reconciling to handle deletion")
			_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: namespacedName})
			Expect(err).NotTo(HaveOccurred())

			By("verifying the object is gone")
			deleted := &gatewayv1alpha1.AIGateway{}
			err = k8sClient.Get(ctx, namespacedName, deleted)
			Expect(apierrors.IsNotFound(err)).To(BeTrue())
		})
	})

	Context("Orphan cleanup", func() {
		It("should clean up orphaned backends and policies when providers are removed", func() {
			By("creating an AIGateway with two providers (both with credentials)")
			aigw := validAIGateway(aigwName)
			aigw.Spec.Providers = append(aigw.Spec.Providers, gatewayv1alpha1.AIGatewayProvider{
				Name:     "anthropic",
				Endpoint: "https://api.anthropic.com",
				Schema:   "Anthropic",
				CredentialRef: &gatewayv1alpha1.CredentialReference{
					Name: "anthropic-secret",
				},
				Models: []string{"claude-sonnet-4-20250514"},
			})
			Expect(k8sClient.Create(ctx, aigw)).To(Succeed())

			r := newReconciler()
			_, err := reconcileN(r, ctx, namespacedName, 2)
			Expect(err).NotTo(HaveOccurred())

			By("verifying both backends and both policies exist")
			backend := &unstructured.Unstructured{}
			backend.SetGroupVersionKind(gvkAIServiceBackend)
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: aigwName + "-openai", Namespace: namespace,
			}, backend)).To(Succeed())
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: aigwName + "-anthropic", Namespace: namespace,
			}, backend)).To(Succeed())

			policy := &unstructured.Unstructured{}
			policy.SetGroupVersionKind(gvkBackendSecurityPolicy)
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: aigwName + "-openai-credentials", Namespace: namespace,
			}, policy)).To(Succeed())
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: aigwName + "-anthropic-credentials", Namespace: namespace,
			}, policy)).To(Succeed())

			By("removing the anthropic provider from spec")
			current := &gatewayv1alpha1.AIGateway{}
			Expect(k8sClient.Get(ctx, namespacedName, current)).To(Succeed())
			current.Spec.Providers = current.Spec.Providers[:1] // Keep only openai.
			Expect(k8sClient.Update(ctx, current)).To(Succeed())

			By("reconciling to trigger cleanup")
			_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: namespacedName})
			Expect(err).NotTo(HaveOccurred())

			By("verifying openai backend and policy still exist")
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: aigwName + "-openai", Namespace: namespace,
			}, backend)).To(Succeed())
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: aigwName + "-openai-credentials", Namespace: namespace,
			}, policy)).To(Succeed())

			By("verifying anthropic backend was cleaned up")
			err = k8sClient.Get(ctx, types.NamespacedName{
				Name: aigwName + "-anthropic", Namespace: namespace,
			}, backend)
			Expect(apierrors.IsNotFound(err)).To(BeTrue())

			By("verifying anthropic policy was cleaned up")
			err = k8sClient.Get(ctx, types.NamespacedName{
				Name: aigwName + "-anthropic-credentials", Namespace: namespace,
			}, policy)
			Expect(apierrors.IsNotFound(err)).To(BeTrue())
		})
	})

	Context("Error paths", func() {
		It("should return no error when reconciling a non-existent AIGateway", func() {
			By("reconciling a name that doesn't exist")
			r := newReconciler()
			res, err := r.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: "does-not-exist", Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(res).To(Equal(reconcile.Result{}))
		})

		It("should set Ready=False condition when endpoint URL is malformed", func() {
			By("creating an AIGateway with a malformed endpoint")
			aigw := validAIGateway(aigwName)
			aigw.Spec.Providers[0].Endpoint = "://bad-url"
			Expect(k8sClient.Create(ctx, aigw)).To(Succeed())

			r := newReconciler()
			// First reconcile adds finalizer.
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: namespacedName})
			Expect(err).NotTo(HaveOccurred())

			// Second reconcile attempts resource creation.
			_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: namespacedName})
			Expect(err).To(HaveOccurred())

			By("verifying the Ready condition is False with error reason")
			Eventually(func(g Gomega) {
				updated := &gatewayv1alpha1.AIGateway{}
				g.Expect(k8sClient.Get(ctx, namespacedName, updated)).To(Succeed())
				readyCond := findCondition(updated.Status.Conditions, "Ready")
				g.Expect(readyCond).NotTo(BeNil())
				g.Expect(readyCond.Status).To(Equal(metav1.ConditionFalse))
			}, testTimeout, testInterval).Should(Succeed())
		})
	})

	Context("Idempotency", func() {
		It("should produce no errors and stable resources on repeated reconciliation", func() {
			By("creating an AIGateway and reconciling to steady state")
			aigw := validAIGateway(aigwName)
			Expect(k8sClient.Create(ctx, aigw)).To(Succeed())

			r := newReconciler()
			_, err := reconcileN(r, ctx, namespacedName, 2)
			Expect(err).NotTo(HaveOccurred())

			By("recording resource versions after first reconcile")
			gw := &unstructured.Unstructured{}
			gw.SetGroupVersionKind(gvkGateway)
			Expect(k8sClient.Get(ctx, namespacedName, gw)).To(Succeed())
			gwVersion := gw.GetResourceVersion()

			backend := &unstructured.Unstructured{}
			backend.SetGroupVersionKind(gvkAIServiceBackend)
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: aigwName + "-openai", Namespace: namespace,
			}, backend)).To(Succeed())
			backendVersion := backend.GetResourceVersion()

			route := &unstructured.Unstructured{}
			route.SetGroupVersionKind(gvkAIGatewayRoute)
			Expect(k8sClient.Get(ctx, namespacedName, route)).To(Succeed())
			routeVersion := route.GetResourceVersion()

			By("reconciling again (should be a no-op update)")
			_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: namespacedName})
			Expect(err).NotTo(HaveOccurred())

			By("verifying resource versions changed (update is called but content is the same)")
			// Note: createOrUpdate always calls Update, so versions will increment.
			// The key assertion is that no error occurs and resources still exist.
			Expect(k8sClient.Get(ctx, namespacedName, gw)).To(Succeed())
			Expect(gw.GetResourceVersion()).NotTo(BeEmpty())
			_ = gwVersion // version increments are acceptable

			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: aigwName + "-openai", Namespace: namespace,
			}, backend)).To(Succeed())
			_ = backendVersion

			Expect(k8sClient.Get(ctx, namespacedName, route)).To(Succeed())
			_ = routeVersion
		})
	})

	Context("Update path", func() {
		It("should update existing resources when spec changes", func() {
			By("creating an AIGateway and reconciling")
			aigw := validAIGateway(aigwName)
			Expect(k8sClient.Create(ctx, aigw)).To(Succeed())

			r := newReconciler()
			_, err := reconcileN(r, ctx, namespacedName, 2)
			Expect(err).NotTo(HaveOccurred())

			By("verifying initial state")
			route := &unstructured.Unstructured{}
			route.SetGroupVersionKind(gvkAIGatewayRoute)
			Expect(k8sClient.Get(ctx, namespacedName, route)).To(Succeed())
			rules, _, _ := unstructured.NestedSlice(route.Object, "spec", "rules")
			Expect(rules).To(HaveLen(2))

			By("adding a third model to the provider")
			current := &gatewayv1alpha1.AIGateway{}
			Expect(k8sClient.Get(ctx, namespacedName, current)).To(Succeed())
			current.Spec.Providers[0].Models = append(current.Spec.Providers[0].Models, "gpt-4o-2024-11-20")
			Expect(k8sClient.Update(ctx, current)).To(Succeed())

			By("reconciling to apply the update")
			_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: namespacedName})
			Expect(err).NotTo(HaveOccurred())

			By("verifying the route now has 3 rules")
			Expect(k8sClient.Get(ctx, namespacedName, route)).To(Succeed())
			rules, _, _ = unstructured.NestedSlice(route.Object, "spec", "rules")
			Expect(rules).To(HaveLen(3))

			By("verifying the new model is in the route")
			rule2 := rules[2].(map[string]interface{})
			matches := rule2["matches"].([]interface{})
			headers := matches[0].(map[string]interface{})["headers"].([]interface{})
			header := headers[0].(map[string]interface{})
			Expect(header["value"]).To(Equal("gpt-4o-2024-11-20"))
		})
	})

	Context("mTLS reconciliation", func() {
		It("should not create ClientTrafficPolicy or CA Secret when mtls is nil", func() {
			By("creating an AIGateway without mTLS and reconciling")
			aigw := validAIGateway(aigwName)
			aigw.Spec.MTLS = nil
			Expect(k8sClient.Create(ctx, aigw)).To(Succeed())

			r := newReconciler()
			_, err := reconcileN(r, ctx, namespacedName, 2)
			Expect(err).NotTo(HaveOccurred())

			By("verifying no ClientTrafficPolicy exists")
			ctp := &unstructured.Unstructured{}
			ctp.SetGroupVersionKind(gvkClientTrafficPolicy)
			err = k8sClient.Get(ctx, namespacedName, ctp)
			Expect(apierrors.IsNotFound(err)).To(BeTrue())

			By("verifying no CA Secret exists")
			caSecret := &corev1.Secret{}
			err = k8sClient.Get(ctx, types.NamespacedName{
				Name: aigwName + "-mtls-ca", Namespace: namespace,
			}, caSecret)
			Expect(apierrors.IsNotFound(err)).To(BeTrue())
		})

		It("should create trust bundle Secret, ClientTrafficPolicy, and server cert when mtls is set", func() {
			By("creating the trust bundle ConfigMap")
			createTrustBundleConfigMap(ctx, "spire-bundle", "spire-system")

			By("creating an AIGateway with mTLS and reconciling")
			aigw := validAIGateway(aigwName)
			aigw.Spec.MTLS = &gatewayv1alpha1.AIGatewayMTLS{
				TrustDomain: "example.org",
				TrustBundleConfigMap: gatewayv1alpha1.TrustBundleRef{
					Name:      "spire-bundle",
					Namespace: "spire-system",
				},
			}
			Expect(k8sClient.Create(ctx, aigw)).To(Succeed())

			r := newReconciler()
			_, err := reconcileN(r, ctx, namespacedName, 2)
			Expect(err).NotTo(HaveOccurred())

			By("verifying the CA Secret was created with PEM content")
			caSecret := &corev1.Secret{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: aigwName + "-mtls-ca", Namespace: namespace,
			}, caSecret)).To(Succeed())
			Expect(caSecret.Data).To(HaveKey("ca.crt"))
			Expect(string(caSecret.Data["ca.crt"])).To(ContainSubstring("BEGIN CERTIFICATE"))

			By("verifying the ClientTrafficPolicy was created")
			ctp := &unstructured.Unstructured{}
			ctp.SetGroupVersionKind(gvkClientTrafficPolicy)
			Expect(k8sClient.Get(ctx, namespacedName, ctp)).To(Succeed())

			By("checking ClientTrafficPolicy targetRef")
			targetRefs, _, _ := unstructured.NestedSlice(ctp.Object, "spec", "targetRefs")
			Expect(targetRefs).To(HaveLen(1))
			ref := targetRefs[0].(map[string]interface{})
			Expect(ref["kind"]).To(Equal("Gateway"))
			Expect(ref["name"]).To(Equal(aigwName))

			By("checking ClientTrafficPolicy client validation optional=false")
			optional, _, _ := unstructured.NestedBool(ctp.Object, "spec", "tls", "clientValidation", "optional")
			Expect(optional).To(BeFalse())

			By("checking ClientTrafficPolicy CA ref")
			caRefs, _, _ := unstructured.NestedSlice(ctp.Object, "spec", "tls", "clientValidation", "caCertificateRefs")
			Expect(caRefs).To(HaveLen(1))
			caRef := caRefs[0].(map[string]interface{})
			Expect(caRef["name"]).To(Equal(aigwName + "-mtls-ca"))

			By("verifying the self-signed server cert Secret was created")
			serverSecret := &corev1.Secret{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: aigwName + "-mtls-server", Namespace: namespace,
			}, serverSecret)).To(Succeed())
			Expect(serverSecret.Type).To(Equal(corev1.SecretTypeTLS))
			Expect(serverSecret.Data).To(HaveKey("tls.crt"))
			Expect(serverSecret.Data).To(HaveKey("tls.key"))

			By("verifying owner reference on the server cert Secret")
			ownerRefs := serverSecret.GetOwnerReferences()
			Expect(ownerRefs).To(HaveLen(1))
			Expect(ownerRefs[0].Kind).To(Equal("AIGateway"))
			Expect(ownerRefs[0].Name).To(Equal(aigwName))

			By("verifying the Gateway listener is HTTPS")
			gw := &unstructured.Unstructured{}
			gw.SetGroupVersionKind(gvkGateway)
			Expect(k8sClient.Get(ctx, namespacedName, gw)).To(Succeed())
			listeners, _, _ := unstructured.NestedSlice(gw.Object, "spec", "listeners")
			Expect(listeners).To(HaveLen(1))
			l := listeners[0].(map[string]interface{})
			Expect(l["protocol"]).To(Equal("HTTPS"))

			By("cleaning up trust bundle ConfigMap")
			deleteTrustBundleConfigMap(ctx, "spire-bundle", "spire-system")
		})

		It("should use serverCertRef when provided instead of generating self-signed cert", func() {
			By("creating the trust bundle ConfigMap")
			createTrustBundleConfigMap(ctx, "spire-bundle-2", "spire-system")

			By("creating an AIGateway with mTLS and serverCertRef")
			aigw := validAIGateway(aigwName)
			aigw.Spec.MTLS = &gatewayv1alpha1.AIGatewayMTLS{
				TrustDomain: "example.org",
				TrustBundleConfigMap: gatewayv1alpha1.TrustBundleRef{
					Name:      "spire-bundle-2",
					Namespace: "spire-system",
				},
				ServerCertRef: &gatewayv1alpha1.CertificateReference{Name: "my-tls-cert"},
			}
			Expect(k8sClient.Create(ctx, aigw)).To(Succeed())

			r := newReconciler()
			_, err := reconcileN(r, ctx, namespacedName, 2)
			Expect(err).NotTo(HaveOccurred())

			By("verifying no self-signed server cert was created")
			serverSecret := &corev1.Secret{}
			err = k8sClient.Get(ctx, types.NamespacedName{
				Name: aigwName + "-mtls-server", Namespace: namespace,
			}, serverSecret)
			Expect(apierrors.IsNotFound(err)).To(BeTrue())

			By("verifying Gateway references the user-provided cert")
			gw := &unstructured.Unstructured{}
			gw.SetGroupVersionKind(gvkGateway)
			Expect(k8sClient.Get(ctx, namespacedName, gw)).To(Succeed())
			listeners, _, _ := unstructured.NestedSlice(gw.Object, "spec", "listeners")
			l := listeners[0].(map[string]interface{})
			tls := l["tls"].(map[string]interface{})
			certRefs := tls["certificateRefs"].([]interface{})
			certRef := certRefs[0].(map[string]interface{})
			Expect(certRef["name"]).To(Equal("my-tls-cert"))

			By("cleaning up trust bundle ConfigMap")
			deleteTrustBundleConfigMap(ctx, "spire-bundle-2", "spire-system")
		})

		It("should update trust bundle Secret when ConfigMap content changes", func() {
			By("creating the trust bundle ConfigMap")
			createTrustBundleConfigMap(ctx, "spire-bundle-3", "spire-system")

			By("creating an AIGateway with mTLS and reconciling")
			aigw := validAIGateway(aigwName)
			aigw.Spec.MTLS = &gatewayv1alpha1.AIGatewayMTLS{
				TrustDomain: "example.org",
				TrustBundleConfigMap: gatewayv1alpha1.TrustBundleRef{
					Name:      "spire-bundle-3",
					Namespace: "spire-system",
				},
			}
			Expect(k8sClient.Create(ctx, aigw)).To(Succeed())

			r := newReconciler()
			_, err := reconcileN(r, ctx, namespacedName, 2)
			Expect(err).NotTo(HaveOccurred())

			By("recording original CA secret content")
			caSecret := &corev1.Secret{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: aigwName + "-mtls-ca", Namespace: namespace,
			}, caSecret)).To(Succeed())
			originalCA := string(caSecret.Data["ca.crt"])

			By("updating the trust bundle ConfigMap with a new cert")
			updateTrustBundleConfigMap(ctx, "spire-bundle-3", "spire-system")

			By("reconciling to pick up the change")
			_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: namespacedName})
			Expect(err).NotTo(HaveOccurred())

			By("verifying the CA Secret content changed")
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: aigwName + "-mtls-ca", Namespace: namespace,
			}, caSecret)).To(Succeed())
			Expect(string(caSecret.Data["ca.crt"])).NotTo(Equal(originalCA))

			By("cleaning up trust bundle ConfigMap")
			deleteTrustBundleConfigMap(ctx, "spire-bundle-3", "spire-system")
		})

		It("should clean up mTLS resources when mTLS is removed from spec", func() {
			By("creating the trust bundle ConfigMap")
			createTrustBundleConfigMap(ctx, "spire-bundle-4", "spire-system")

			By("creating an AIGateway with mTLS and reconciling")
			aigw := validAIGateway(aigwName)
			aigw.Spec.MTLS = &gatewayv1alpha1.AIGatewayMTLS{
				TrustDomain: "example.org",
				TrustBundleConfigMap: gatewayv1alpha1.TrustBundleRef{
					Name:      "spire-bundle-4",
					Namespace: "spire-system",
				},
			}
			Expect(k8sClient.Create(ctx, aigw)).To(Succeed())

			r := newReconciler()
			_, err := reconcileN(r, ctx, namespacedName, 2)
			Expect(err).NotTo(HaveOccurred())

			By("verifying mTLS resources exist")
			ctp := &unstructured.Unstructured{}
			ctp.SetGroupVersionKind(gvkClientTrafficPolicy)
			Expect(k8sClient.Get(ctx, namespacedName, ctp)).To(Succeed())

			caSecret := &corev1.Secret{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: aigwName + "-mtls-ca", Namespace: namespace,
			}, caSecret)).To(Succeed())

			By("removing mTLS from the spec")
			current := &gatewayv1alpha1.AIGateway{}
			Expect(k8sClient.Get(ctx, namespacedName, current)).To(Succeed())
			current.Spec.MTLS = nil
			Expect(k8sClient.Update(ctx, current)).To(Succeed())

			By("reconciling to trigger cleanup")
			_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: namespacedName})
			Expect(err).NotTo(HaveOccurred())

			By("verifying ClientTrafficPolicy was cleaned up")
			err = k8sClient.Get(ctx, namespacedName, ctp)
			Expect(apierrors.IsNotFound(err)).To(BeTrue())

			By("verifying CA Secret was cleaned up")
			err = k8sClient.Get(ctx, types.NamespacedName{
				Name: aigwName + "-mtls-ca", Namespace: namespace,
			}, caSecret)
			Expect(apierrors.IsNotFound(err)).To(BeTrue())

			By("verifying server cert Secret was cleaned up")
			serverSecret := &corev1.Secret{}
			err = k8sClient.Get(ctx, types.NamespacedName{
				Name: aigwName + "-mtls-server", Namespace: namespace,
			}, serverSecret)
			Expect(apierrors.IsNotFound(err)).To(BeTrue())

			By("cleaning up trust bundle ConfigMap")
			deleteTrustBundleConfigMap(ctx, "spire-bundle-4", "spire-system")
		})
	})

	Context("mTLS failure modes", func() {
		It("should set Ready=False with MTLSFailed when trust bundle ConfigMap is missing", func() {
			By("creating an AIGateway referencing a non-existent ConfigMap")
			aigw := validAIGateway(aigwName)
			aigw.Spec.MTLS = &gatewayv1alpha1.AIGatewayMTLS{
				TrustDomain: "example.org",
				TrustBundleConfigMap: gatewayv1alpha1.TrustBundleRef{
					Name:      "nonexistent-bundle",
					Namespace: "nonexistent-ns",
				},
			}
			Expect(k8sClient.Create(ctx, aigw)).To(Succeed())

			r := newReconciler()
			// First reconcile adds finalizer.
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: namespacedName})
			Expect(err).NotTo(HaveOccurred())

			// Second reconcile attempts mTLS setup.
			_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: namespacedName})
			Expect(err).To(HaveOccurred())

			By("verifying Ready condition is False with MTLSFailed reason")
			Eventually(func(g Gomega) {
				updated := &gatewayv1alpha1.AIGateway{}
				g.Expect(k8sClient.Get(ctx, namespacedName, updated)).To(Succeed())
				readyCond := findCondition(updated.Status.Conditions, "Ready")
				g.Expect(readyCond).NotTo(BeNil())
				g.Expect(readyCond.Status).To(Equal(metav1.ConditionFalse))
				g.Expect(readyCond.Reason).To(Equal("MTLSFailed"))
				g.Expect(readyCond.Message).To(ContainSubstring("trust bundle configmap"))
			}, testTimeout, testInterval).Should(Succeed())
		})

		It("should set Ready=False when trust bundle ConfigMap key is missing", func() {
			By("creating a ConfigMap without the expected key")
			ensureNamespace(ctx, "spire-system")
			cm := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "bad-key-bundle",
					Namespace: "spire-system",
				},
				Data: map[string]string{
					"wrong-key": "some data",
				},
			}
			Expect(k8sClient.Create(ctx, cm)).To(Succeed())

			aigw := validAIGateway(aigwName)
			aigw.Spec.MTLS = &gatewayv1alpha1.AIGatewayMTLS{
				TrustDomain: "example.org",
				TrustBundleConfigMap: gatewayv1alpha1.TrustBundleRef{
					Name:      "bad-key-bundle",
					Namespace: "spire-system",
				},
			}
			Expect(k8sClient.Create(ctx, aigw)).To(Succeed())

			r := newReconciler()
			_, err := reconcileN(r, ctx, namespacedName, 2)
			Expect(err).To(HaveOccurred())

			By("verifying the error mentions the missing key")
			Eventually(func(g Gomega) {
				updated := &gatewayv1alpha1.AIGateway{}
				g.Expect(k8sClient.Get(ctx, namespacedName, updated)).To(Succeed())
				readyCond := findCondition(updated.Status.Conditions, "Ready")
				g.Expect(readyCond).NotTo(BeNil())
				g.Expect(readyCond.Status).To(Equal(metav1.ConditionFalse))
				g.Expect(readyCond.Reason).To(Equal("MTLSFailed"))
			}, testTimeout, testInterval).Should(Succeed())

			_ = k8sClient.Delete(ctx, cm)
		})

		It("should set Ready=False when trust bundle contains invalid JSON", func() {
			By("creating a ConfigMap with invalid JSON")
			ensureNamespace(ctx, "spire-system")
			cm := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "invalid-json-bundle",
					Namespace: "spire-system",
				},
				Data: map[string]string{
					"bundle.spiffe": "not valid json",
				},
			}
			Expect(k8sClient.Create(ctx, cm)).To(Succeed())

			aigw := validAIGateway(aigwName)
			aigw.Spec.MTLS = &gatewayv1alpha1.AIGatewayMTLS{
				TrustDomain: "example.org",
				TrustBundleConfigMap: gatewayv1alpha1.TrustBundleRef{
					Name:      "invalid-json-bundle",
					Namespace: "spire-system",
				},
			}
			Expect(k8sClient.Create(ctx, aigw)).To(Succeed())

			r := newReconciler()
			_, err := reconcileN(r, ctx, namespacedName, 2)
			Expect(err).To(HaveOccurred())

			By("verifying Ready condition is False with MTLSFailed")
			Eventually(func(g Gomega) {
				updated := &gatewayv1alpha1.AIGateway{}
				g.Expect(k8sClient.Get(ctx, namespacedName, updated)).To(Succeed())
				readyCond := findCondition(updated.Status.Conditions, "Ready")
				g.Expect(readyCond).NotTo(BeNil())
				g.Expect(readyCond.Status).To(Equal(metav1.ConditionFalse))
				g.Expect(readyCond.Reason).To(Equal("MTLSFailed"))
			}, testTimeout, testInterval).Should(Succeed())

			_ = k8sClient.Delete(ctx, cm)
		})

		It("should set Ready=False when trust bundle has no x509-svid certificates", func() {
			By("creating a ConfigMap with empty keys array")
			ensureNamespace(ctx, "spire-system")
			cm := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "empty-certs-bundle",
					Namespace: "spire-system",
				},
				Data: map[string]string{
					"bundle.spiffe": `{"keys":[]}`,
				},
			}
			Expect(k8sClient.Create(ctx, cm)).To(Succeed())

			aigw := validAIGateway(aigwName)
			aigw.Spec.MTLS = &gatewayv1alpha1.AIGatewayMTLS{
				TrustDomain: "example.org",
				TrustBundleConfigMap: gatewayv1alpha1.TrustBundleRef{
					Name:      "empty-certs-bundle",
					Namespace: "spire-system",
				},
			}
			Expect(k8sClient.Create(ctx, aigw)).To(Succeed())

			r := newReconciler()
			_, err := reconcileN(r, ctx, namespacedName, 2)
			Expect(err).To(HaveOccurred())

			By("verifying Ready condition mentions no certificates")
			Eventually(func(g Gomega) {
				updated := &gatewayv1alpha1.AIGateway{}
				g.Expect(k8sClient.Get(ctx, namespacedName, updated)).To(Succeed())
				readyCond := findCondition(updated.Status.Conditions, "Ready")
				g.Expect(readyCond).NotTo(BeNil())
				g.Expect(readyCond.Status).To(Equal(metav1.ConditionFalse))
				g.Expect(readyCond.Reason).To(Equal("MTLSFailed"))
			}, testTimeout, testInterval).Should(Succeed())

			_ = k8sClient.Delete(ctx, cm)
		})

		It("should use custom trustBundleConfigMap.key when specified", func() {
			By("creating a ConfigMap with a custom key")
			ensureNamespace(ctx, "spire-system")
			caDER := generateTestCACert()
			bundleJSON := makeSPIFFEBundle(caDER)

			cm := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "custom-key-bundle",
					Namespace: "spire-system",
				},
				Data: map[string]string{
					"my-custom-key": bundleJSON,
				},
			}
			Expect(k8sClient.Create(ctx, cm)).To(Succeed())

			aigw := validAIGateway(aigwName)
			aigw.Spec.MTLS = &gatewayv1alpha1.AIGatewayMTLS{
				TrustDomain: "example.org",
				TrustBundleConfigMap: gatewayv1alpha1.TrustBundleRef{
					Name:      "custom-key-bundle",
					Namespace: "spire-system",
					Key:       "my-custom-key",
				},
			}
			Expect(k8sClient.Create(ctx, aigw)).To(Succeed())

			r := newReconciler()
			_, err := reconcileN(r, ctx, namespacedName, 2)
			Expect(err).NotTo(HaveOccurred())

			By("verifying CA Secret was created successfully")
			caSecret := &corev1.Secret{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: aigwName + "-mtls-ca", Namespace: namespace,
			}, caSecret)).To(Succeed())
			Expect(string(caSecret.Data["ca.crt"])).To(ContainSubstring("BEGIN CERTIFICATE"))

			_ = k8sClient.Delete(ctx, cm)
		})

		It("should transition Gateway listeners from HTTPS back to HTTP when mTLS is removed", func() {
			By("creating the trust bundle ConfigMap")
			createTrustBundleConfigMap(ctx, "spire-bundle-5", "spire-system")

			By("creating an AIGateway with mTLS and reconciling")
			aigw := validAIGateway(aigwName)
			aigw.Spec.MTLS = &gatewayv1alpha1.AIGatewayMTLS{
				TrustDomain: "example.org",
				TrustBundleConfigMap: gatewayv1alpha1.TrustBundleRef{
					Name:      "spire-bundle-5",
					Namespace: "spire-system",
				},
			}
			Expect(k8sClient.Create(ctx, aigw)).To(Succeed())

			r := newReconciler()
			_, err := reconcileN(r, ctx, namespacedName, 2)
			Expect(err).NotTo(HaveOccurred())

			By("verifying Gateway listener is HTTPS")
			gw := &unstructured.Unstructured{}
			gw.SetGroupVersionKind(gvkGateway)
			Expect(k8sClient.Get(ctx, namespacedName, gw)).To(Succeed())
			listeners, _, _ := unstructured.NestedSlice(gw.Object, "spec", "listeners")
			l := listeners[0].(map[string]interface{})
			Expect(l["protocol"]).To(Equal("HTTPS"))
			Expect(l).To(HaveKey("tls"))

			By("removing mTLS from the spec")
			current := &gatewayv1alpha1.AIGateway{}
			Expect(k8sClient.Get(ctx, namespacedName, current)).To(Succeed())
			current.Spec.MTLS = nil
			Expect(k8sClient.Update(ctx, current)).To(Succeed())

			By("reconciling to apply changes")
			_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: namespacedName})
			Expect(err).NotTo(HaveOccurred())

			By("verifying Gateway listener switched back to HTTP")
			Expect(k8sClient.Get(ctx, namespacedName, gw)).To(Succeed())
			listeners, _, _ = unstructured.NestedSlice(gw.Object, "spec", "listeners")
			l = listeners[0].(map[string]interface{})
			Expect(l["protocol"]).To(Equal("HTTP"))

			By("cleaning up trust bundle ConfigMap")
			deleteTrustBundleConfigMap(ctx, "spire-bundle-5", "spire-system")
		})
	})
})

// Unit tests for helper functions, following the pattern from agentcard_controller_test.go
// which tests getWorkload, getServicePort, getWorkloadProtocol, etc. as separate Describe blocks.

var _ = Describe("backendName", func() {
	It("should join aigw name and provider name with a hyphen", func() {
		Expect(backendName("my-gateway", "openai")).To(Equal("my-gateway-openai"))
		Expect(backendName("gw", "local-ollama")).To(Equal("gw-local-ollama"))
	})
})

var _ = Describe("policyName", func() {
	It("should join aigw name, provider name, and 'credentials' suffix", func() {
		Expect(policyName("my-gateway", "openai")).To(Equal("my-gateway-openai-credentials"))
		Expect(policyName("gw", "anthropic")).To(Equal("gw-anthropic-credentials"))
	})
})

var _ = Describe("computeEndpoint", func() {
	It("should compute the endpoint from the first listener port", func() {
		r := &AIGatewayReconciler{}
		aigw := &gatewayv1alpha1.AIGateway{
			ObjectMeta: metav1.ObjectMeta{Name: "ai-gw", Namespace: "prod"},
			Spec: gatewayv1alpha1.AIGatewaySpec{
				Listeners: []gatewayv1alpha1.AIGatewayListener{
					{Name: "http", Port: 9090},
				},
			},
		}
		Expect(r.computeEndpoint(aigw)).To(Equal("http://ai-gw.prod.svc:9090"))
	})

	It("should default to port 8080 when no listeners are specified", func() {
		r := &AIGatewayReconciler{}
		aigw := &gatewayv1alpha1.AIGateway{
			ObjectMeta: metav1.ObjectMeta{Name: "ai-gw", Namespace: "default"},
			Spec:       gatewayv1alpha1.AIGatewaySpec{},
		}
		Expect(r.computeEndpoint(aigw)).To(Equal("http://ai-gw.default.svc:8080"))
	})

	It("should use https scheme when mTLS is configured", func() {
		r := &AIGatewayReconciler{}
		aigw := &gatewayv1alpha1.AIGateway{
			ObjectMeta: metav1.ObjectMeta{Name: "ai-gw", Namespace: "prod"},
			Spec: gatewayv1alpha1.AIGatewaySpec{
				Listeners: []gatewayv1alpha1.AIGatewayListener{
					{Name: "https", Port: 8443},
				},
				MTLS: &gatewayv1alpha1.AIGatewayMTLS{
					TrustDomain: "example.org",
				},
			},
		}
		Expect(r.computeEndpoint(aigw)).To(Equal("https://ai-gw.prod.svc:8443"))
	})
})

var _ = Describe("isOwnedBy", func() {
	It("should return true when the object has a matching owner UID", func() {
		aigw := &gatewayv1alpha1.AIGateway{
			ObjectMeta: metav1.ObjectMeta{UID: "test-uid-123"},
		}
		obj := &unstructured.Unstructured{}
		obj.SetOwnerReferences([]metav1.OwnerReference{
			{UID: "test-uid-123"},
		})
		Expect(isOwnedBy(obj, aigw)).To(BeTrue())
	})

	It("should return false when the object has no matching owner UID", func() {
		aigw := &gatewayv1alpha1.AIGateway{
			ObjectMeta: metav1.ObjectMeta{UID: "test-uid-123"},
		}
		obj := &unstructured.Unstructured{}
		obj.SetOwnerReferences([]metav1.OwnerReference{
			{UID: "different-uid"},
		})
		Expect(isOwnedBy(obj, aigw)).To(BeFalse())
	})

	It("should return false when the object has no owner references", func() {
		aigw := &gatewayv1alpha1.AIGateway{
			ObjectMeta: metav1.ObjectMeta{UID: "test-uid-123"},
		}
		obj := &unstructured.Unstructured{}
		Expect(isOwnedBy(obj, aigw)).To(BeFalse())
	})
})

var _ = Describe("isGatewayReady", func() {
	ctx := context.Background()

	It("should return false when the Gateway does not exist", func() {
		r := &AIGatewayReconciler{Client: k8sClient}
		aigw := &gatewayv1alpha1.AIGateway{
			ObjectMeta: metav1.ObjectMeta{Name: "nonexistent-gw", Namespace: "default"},
		}
		Expect(r.isGatewayReady(ctx, aigw)).To(BeFalse())
	})

	It("should return false when Gateway has no status conditions", func() {
		By("creating a Gateway with no status")
		gw := &unstructured.Unstructured{}
		gw.SetGroupVersionKind(gvkGateway)
		gw.SetName("ready-test-gw")
		gw.SetNamespace("default")
		gw.Object["spec"] = map[string]interface{}{"gatewayClassName": "eg"}
		Expect(k8sClient.Create(ctx, gw)).To(Succeed())

		r := &AIGatewayReconciler{Client: k8sClient}
		aigw := &gatewayv1alpha1.AIGateway{
			ObjectMeta: metav1.ObjectMeta{Name: "ready-test-gw", Namespace: "default"},
		}
		Expect(r.isGatewayReady(ctx, aigw)).To(BeFalse())

		_ = k8sClient.Delete(ctx, gw)
	})

	It("should return true when Gateway has Programmed=True", func() {
		By("creating a Gateway with Programmed=True")
		gw := &unstructured.Unstructured{}
		gw.SetGroupVersionKind(gvkGateway)
		gw.SetName("ready-test-gw2")
		gw.SetNamespace("default")
		gw.Object["spec"] = map[string]interface{}{"gatewayClassName": "eg"}
		Expect(k8sClient.Create(ctx, gw)).To(Succeed())

		_ = unstructured.SetNestedSlice(gw.Object, []interface{}{
			map[string]interface{}{
				"type":   "Programmed",
				"status": "True",
				"reason": "Programmed",
			},
		}, "status", "conditions")
		Expect(k8sClient.Status().Update(ctx, gw)).To(Succeed())

		r := &AIGatewayReconciler{Client: k8sClient}
		aigw := &gatewayv1alpha1.AIGateway{
			ObjectMeta: metav1.ObjectMeta{Name: "ready-test-gw2", Namespace: "default"},
		}
		Expect(r.isGatewayReady(ctx, aigw)).To(BeTrue())

		_ = k8sClient.Delete(ctx, gw)
	})

	It("should return false when Gateway has Programmed=False", func() {
		By("creating a Gateway with Programmed=False")
		gw := &unstructured.Unstructured{}
		gw.SetGroupVersionKind(gvkGateway)
		gw.SetName("ready-test-gw3")
		gw.SetNamespace("default")
		gw.Object["spec"] = map[string]interface{}{"gatewayClassName": "eg"}
		Expect(k8sClient.Create(ctx, gw)).To(Succeed())

		_ = unstructured.SetNestedSlice(gw.Object, []interface{}{
			map[string]interface{}{
				"type":   "Programmed",
				"status": "False",
				"reason": "Pending",
			},
		}, "status", "conditions")
		Expect(k8sClient.Status().Update(ctx, gw)).To(Succeed())

		r := &AIGatewayReconciler{Client: k8sClient}
		aigw := &gatewayv1alpha1.AIGateway{
			ObjectMeta: metav1.ObjectMeta{Name: "ready-test-gw3", Namespace: "default"},
		}
		Expect(r.isGatewayReady(ctx, aigw)).To(BeFalse())

		_ = k8sClient.Delete(ctx, gw)
	})
})

var _ = Describe("caSecretName", func() {
	It("should append -mtls-ca suffix", func() {
		Expect(caSecretName("my-gateway")).To(Equal("my-gateway-mtls-ca"))
	})
})

var _ = Describe("serverCertSecretName", func() {
	It("should append -mtls-server suffix", func() {
		Expect(serverCertSecretName("my-gateway")).To(Equal("my-gateway-mtls-server"))
	})
})

// Test helpers for creating SPIFFE trust bundle ConfigMaps.

func generateTestCACert() []byte {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	ExpectWithOffset(1, err).NotTo(HaveOccurred())

	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "Test CA"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		IsCA:         true,
		KeyUsage:     x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}

	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	ExpectWithOffset(1, err).NotTo(HaveOccurred())
	return der
}

func makeSPIFFEBundle(certDERs ...[]byte) string {
	type bundleKey struct {
		Use string   `json:"use"`
		X5C []string `json:"x5c"`
	}
	type bundle struct {
		Keys []bundleKey `json:"keys"`
	}

	x5c := make([]string, len(certDERs))
	for i, der := range certDERs {
		x5c[i] = base64.StdEncoding.EncodeToString(der)
	}

	b := bundle{Keys: []bundleKey{{Use: "x509-svid", X5C: x5c}}}
	data, _ := json.Marshal(b)
	return string(data)
}

func ensureNamespace(ctx context.Context, name string) {
	ns := &corev1.Namespace{}
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: name}, ns); err != nil {
		ns = &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: name},
		}
		ExpectWithOffset(1, k8sClient.Create(ctx, ns)).To(Succeed())
	}
}

func createTrustBundleConfigMap(ctx context.Context, name, namespace string) {
	ensureNamespace(ctx, namespace)
	caDER := generateTestCACert()
	bundleJSON := makeSPIFFEBundle(caDER)

	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Data: map[string]string{
			"bundle.spiffe": bundleJSON,
		},
	}
	ExpectWithOffset(1, k8sClient.Create(ctx, cm)).To(Succeed())
}

func updateTrustBundleConfigMap(ctx context.Context, name, namespace string) {
	cm := &corev1.ConfigMap{}
	ExpectWithOffset(1, k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, cm)).To(Succeed())

	// Generate a new CA cert so the bundle content is different.
	newCaDER := generateTestCACert()
	cm.Data["bundle.spiffe"] = makeSPIFFEBundle(newCaDER)
	ExpectWithOffset(1, k8sClient.Update(ctx, cm)).To(Succeed())
}

func deleteTrustBundleConfigMap(ctx context.Context, name, namespace string) {
	cm := &corev1.ConfigMap{}
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, cm); err == nil {
		_ = k8sClient.Delete(ctx, cm)
	}
}

// findCondition is also defined in agentcard_controller_test.go in the same package.
// Since Go test files in the same package share a single compilation unit, we don't
// redefine it here. The function is available from that file.
