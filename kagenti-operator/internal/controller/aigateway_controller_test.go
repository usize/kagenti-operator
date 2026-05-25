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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	gatewayv1alpha1 "github.com/kagenti/operator/api/gateway/v1alpha1"
)

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
		// Clean up the AIGateway if it exists.
		aigw := &gatewayv1alpha1.AIGateway{}
		if err := k8sClient.Get(ctx, namespacedName, aigw); err == nil {
			// Remove finalizer for clean deletion.
			aigw.Finalizers = nil
			_ = k8sClient.Update(ctx, aigw)
			_ = k8sClient.Delete(ctx, aigw)
		}

		// Clean up unstructured resources.
		for _, gvk := range []schema.GroupVersionKind{gvkGateway, gvkAIServiceBackend, gvkBackendSecurityPolicy, gvkAIGatewayRoute} {
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
	})

	Context("Finalizer management", func() {
		It("should add finalizer on first reconcile", func() {
			aigw := validAIGateway(aigwName)
			Expect(k8sClient.Create(ctx, aigw)).To(Succeed())

			r := newReconciler()
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: namespacedName})
			Expect(err).NotTo(HaveOccurred())

			updated := &gatewayv1alpha1.AIGateway{}
			Expect(k8sClient.Get(ctx, namespacedName, updated)).To(Succeed())
			Expect(updated.Finalizers).To(ContainElement(AIGatewayFinalizer))
		})
	})

	Context("Gateway creation", func() {
		It("should create Gateway with correct gatewayClassName and listeners", func() {
			aigw := validAIGateway(aigwName)
			Expect(k8sClient.Create(ctx, aigw)).To(Succeed())

			r := newReconciler()
			// First reconcile adds finalizer.
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: namespacedName})
			Expect(err).NotTo(HaveOccurred())
			// Second reconcile creates resources.
			_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: namespacedName})
			Expect(err).NotTo(HaveOccurred())

			gw := &unstructured.Unstructured{}
			gw.SetGroupVersionKind(gvkGateway)
			Expect(k8sClient.Get(ctx, namespacedName, gw)).To(Succeed())

			className, _, _ := unstructured.NestedString(gw.Object, "spec", "gatewayClassName")
			Expect(className).To(Equal("eg"))

			listeners, _, _ := unstructured.NestedSlice(gw.Object, "spec", "listeners")
			Expect(listeners).To(HaveLen(1))
			l := listeners[0].(map[string]interface{})
			Expect(l["name"]).To(Equal("http"))
			Expect(l["port"]).To(BeNumerically("==", 8080))
			Expect(l["protocol"]).To(Equal("HTTP"))
		})
	})

	Context("AIServiceBackend creation", func() {
		It("should create AIServiceBackend with external endpoint (Backend kind)", func() {
			aigw := validAIGateway(aigwName)
			Expect(k8sClient.Create(ctx, aigw)).To(Succeed())

			r := newReconciler()
			_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: namespacedName})
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: namespacedName})
			Expect(err).NotTo(HaveOccurred())

			backend := &unstructured.Unstructured{}
			backend.SetGroupVersionKind(gvkAIServiceBackend)
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: aigwName + "-openai", Namespace: namespace,
			}, backend)).To(Succeed())

			schemaName, _, _ := unstructured.NestedString(backend.Object, "spec", "schema", "name")
			Expect(schemaName).To(Equal("OpenAI"))

			backendKind, _, _ := unstructured.NestedString(backend.Object, "spec", "backendRef", "kind")
			Expect(backendKind).To(Equal("Backend"))

			backendGroup, _, _ := unstructured.NestedString(backend.Object, "spec", "backendRef", "group")
			Expect(backendGroup).To(Equal("gateway.envoyproxy.io"))
		})

		It("should create AIServiceBackend with in-cluster endpoint (Service kind)", func() {
			aigw := validAIGateway(aigwName)
			aigw.Spec.Providers[0].Name = "local"
			aigw.Spec.Providers[0].Endpoint = "http://ollama.default.svc:11434"
			aigw.Spec.Providers[0].Schema = "OpenAI"
			aigw.Spec.Providers[0].CredentialRef = nil
			Expect(k8sClient.Create(ctx, aigw)).To(Succeed())

			r := newReconciler()
			_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: namespacedName})
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: namespacedName})
			Expect(err).NotTo(HaveOccurred())

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
			aigw := validAIGateway(aigwName)
			// Add a second provider without credentials.
			aigw.Spec.Providers = append(aigw.Spec.Providers, gatewayv1alpha1.AIGatewayProvider{
				Name:     "ollama",
				Endpoint: "http://ollama.default.svc:11434",
				Schema:   "OpenAI",
				Models:   []string{"llama3"},
			})
			Expect(k8sClient.Create(ctx, aigw)).To(Succeed())

			r := newReconciler()
			_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: namespacedName})
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: namespacedName})
			Expect(err).NotTo(HaveOccurred())

			// openai should have a policy.
			policy := &unstructured.Unstructured{}
			policy.SetGroupVersionKind(gvkBackendSecurityPolicy)
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: aigwName + "-openai-credentials", Namespace: namespace,
			}, policy)).To(Succeed())

			secretName, _, _ := unstructured.NestedString(policy.Object, "spec", "apiKey", "secretRef", "name")
			Expect(secretName).To(Equal("openai-secret"))

			// ollama should NOT have a policy.
			ollamaPolicy := &unstructured.Unstructured{}
			ollamaPolicy.SetGroupVersionKind(gvkBackendSecurityPolicy)
			err = k8sClient.Get(ctx, types.NamespacedName{
				Name: aigwName + "-ollama-credentials", Namespace: namespace,
			}, ollamaPolicy)
			Expect(err).To(HaveOccurred())
		})
	})

	Context("AIGatewayRoute creation", func() {
		It("should create AIGatewayRoute with one rule per model", func() {
			aigw := validAIGateway(aigwName)
			Expect(k8sClient.Create(ctx, aigw)).To(Succeed())

			r := newReconciler()
			_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: namespacedName})
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: namespacedName})
			Expect(err).NotTo(HaveOccurred())

			route := &unstructured.Unstructured{}
			route.SetGroupVersionKind(gvkAIGatewayRoute)
			Expect(k8sClient.Get(ctx, namespacedName, route)).To(Succeed())

			rules, _, _ := unstructured.NestedSlice(route.Object, "spec", "rules")
			Expect(rules).To(HaveLen(2)) // gpt-4o and gpt-4o-mini

			// Verify first rule has correct header match and backendRef.
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
		})
	})

	Context("Status updates", func() {
		It("should update status with provider and model counts and observedGeneration", func() {
			aigw := validAIGateway(aigwName)
			Expect(k8sClient.Create(ctx, aigw)).To(Succeed())

			r := newReconciler()
			_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: namespacedName})
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: namespacedName})
			Expect(err).NotTo(HaveOccurred())

			updated := &gatewayv1alpha1.AIGateway{}
			Expect(k8sClient.Get(ctx, namespacedName, updated)).To(Succeed())
			Expect(updated.Status.ConfiguredProviders).To(Equal(int32(1)))
			Expect(updated.Status.ConfiguredModels).To(Equal(int32(2)))
			Expect(updated.Status.ObservedGeneration).To(Equal(updated.Generation))
		})

		It("should set Ready=False when Gateway lacks Programmed condition", func() {
			aigw := validAIGateway(aigwName)
			Expect(k8sClient.Create(ctx, aigw)).To(Succeed())

			r := newReconciler()
			_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: namespacedName})
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: namespacedName})
			Expect(err).NotTo(HaveOccurred())

			updated := &gatewayv1alpha1.AIGateway{}
			Expect(k8sClient.Get(ctx, namespacedName, updated)).To(Succeed())

			var readyCond *metav1.Condition
			for i := range updated.Status.Conditions {
				if updated.Status.Conditions[i].Type == "Ready" {
					readyCond = &updated.Status.Conditions[i]
					break
				}
			}
			Expect(readyCond).NotTo(BeNil())
			Expect(readyCond.Status).To(Equal(metav1.ConditionFalse))
			Expect(readyCond.Reason).To(Equal("GatewayNotReady"))
		})

		It("should set Ready=True and compute endpoint when Gateway has Programmed=True", func() {
			aigw := validAIGateway(aigwName)
			Expect(k8sClient.Create(ctx, aigw)).To(Succeed())

			r := newReconciler()
			// Add finalizer.
			_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: namespacedName})
			// Create resources.
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: namespacedName})
			Expect(err).NotTo(HaveOccurred())

			// Simulate Gateway becoming Programmed.
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

			// Reconcile again to pick up gateway status.
			_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: namespacedName})
			Expect(err).NotTo(HaveOccurred())

			updated := &gatewayv1alpha1.AIGateway{}
			Expect(k8sClient.Get(ctx, namespacedName, updated)).To(Succeed())

			var readyCond *metav1.Condition
			for i := range updated.Status.Conditions {
				if updated.Status.Conditions[i].Type == "Ready" {
					readyCond = &updated.Status.Conditions[i]
					break
				}
			}
			Expect(readyCond).NotTo(BeNil())
			Expect(readyCond.Status).To(Equal(metav1.ConditionTrue))
			Expect(readyCond.Reason).To(Equal("Ready"))
			Expect(updated.Status.Endpoint).To(Equal("http://test-aigw.default.svc:8080"))
		})
	})

	Context("Deletion handling", func() {
		It("should remove finalizer on deletion", func() {
			aigw := validAIGateway(aigwName)
			Expect(k8sClient.Create(ctx, aigw)).To(Succeed())

			r := newReconciler()
			// Add finalizer.
			_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: namespacedName})
			// Create resources.
			_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: namespacedName})

			// Delete the AIGateway.
			Expect(k8sClient.Delete(ctx, aigw)).To(Succeed())

			// Reconcile handles deletion.
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: namespacedName})
			Expect(err).NotTo(HaveOccurred())

			// The object should be gone (finalizer removed, deletion proceeds).
			deleted := &gatewayv1alpha1.AIGateway{}
			err = k8sClient.Get(ctx, namespacedName, deleted)
			Expect(err).To(HaveOccurred())
		})
	})

	Context("Orphan cleanup", func() {
		It("should clean up orphaned backends when providers are removed from spec", func() {
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

			// Create a secret for the credential ref (not strictly needed but good practice).
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "anthropic-secret",
					Namespace: namespace,
				},
				Data: map[string][]byte{"api-key": []byte("test")},
			}
			_ = k8sClient.Create(ctx, secret)

			r := newReconciler()
			_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: namespacedName})
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: namespacedName})
			Expect(err).NotTo(HaveOccurred())

			// Verify both backends exist.
			backend := &unstructured.Unstructured{}
			backend.SetGroupVersionKind(gvkAIServiceBackend)
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: aigwName + "-openai", Namespace: namespace,
			}, backend)).To(Succeed())
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: aigwName + "-anthropic", Namespace: namespace,
			}, backend)).To(Succeed())

			// Remove anthropic provider.
			current := &gatewayv1alpha1.AIGateway{}
			Expect(k8sClient.Get(ctx, namespacedName, current)).To(Succeed())
			current.Spec.Providers = current.Spec.Providers[:1] // Keep only openai.
			Expect(k8sClient.Update(ctx, current)).To(Succeed())

			// Reconcile to trigger cleanup.
			_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: namespacedName})
			Expect(err).NotTo(HaveOccurred())

			// openai backend should still exist.
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: aigwName + "-openai", Namespace: namespace,
			}, backend)).To(Succeed())

			// anthropic backend should be gone.
			err = k8sClient.Get(ctx, types.NamespacedName{
				Name: aigwName + "-anthropic", Namespace: namespace,
			}, backend)
			Expect(err).To(HaveOccurred())
		})
	})
})
