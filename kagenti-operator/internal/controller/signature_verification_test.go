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
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"sort"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	agentv1alpha1 "github.com/kagenti/operator/api/v1alpha1"
	"github.com/kagenti/operator/internal/signature"
)

var _ = Describe("Signature Verification", func() {
	const (
		timeout  = time.Second * 10
		interval = time.Millisecond * 250
	)

	Context("Signed AgentCard — Valid JWS Signature", func() {
		const (
			agentName     = "sig-valid-agent"
			agentCardName = "sig-valid-card"
			namespace     = "default"
			secretName    = "sig-valid-keys"
		)

		var (
			rsaPrivKey *rsa.PrivateKey
			pubKeyPEM  []byte
		)

		ctx := context.Background()

		BeforeEach(func() {
			By("generating an RSA key pair")
			var err error
			rsaPrivKey, err = rsa.GenerateKey(rand.Reader, 2048)
			Expect(err).NotTo(HaveOccurred())

			pubDER, err := x509.MarshalPKIXPublicKey(&rsaPrivKey.PublicKey)
			Expect(err).NotTo(HaveOccurred())
			pubKeyPEM = pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER})

			By("creating the public key Secret")
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      secretName,
					Namespace: namespace,
				},
				Data: map[string][]byte{
					"my-signing-key": pubKeyPEM,
				},
			}
			Expect(k8sClient.Create(ctx, secret)).To(Succeed())
		})

		AfterEach(func() {
			By("cleaning up test resources")
			cleanupResource(ctx, &agentv1alpha1.AgentCard{}, agentCardName, namespace)
			cleanupResource(ctx, &agentv1alpha1.Agent{}, agentName, namespace)
			cleanupResource(ctx, &corev1.Service{}, agentName, namespace)
			cleanupResource(ctx, &corev1.Secret{}, secretName, namespace)
		})

		It("should set validSignature=true and SignatureVerified condition for a correctly signed card", func() {
			By("creating an Agent")
			agent := &agentv1alpha1.Agent{
				ObjectMeta: metav1.ObjectMeta{
					Name:      agentName,
					Namespace: namespace,
					Labels: map[string]string{
						"app.kubernetes.io/name": agentName,
						LabelAgentType:           LabelValueAgent,
						LabelAgentProtocol:       "a2a",
					},
				},
				Spec: agentv1alpha1.AgentSpec{
					PodTemplateSpec: &corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{Name: "agent", Image: "test-image:latest"},
							},
						},
					},
					ImageSource: agentv1alpha1.ImageSource{Image: ptr.To("test-image:latest")},
				},
			}
			Expect(k8sClient.Create(ctx, agent)).To(Succeed())
			Eventually(func() error {
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: agentName, Namespace: namespace}, agent); err != nil {
					return err
				}
				agent.Status.DeploymentStatus = &agentv1alpha1.DeploymentStatus{Phase: agentv1alpha1.PhaseReady}
				return k8sClient.Status().Update(ctx, agent)
			}).Should(Succeed())

			By("creating a Service for the Agent")
			service := &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{Name: agentName, Namespace: namespace},
				Spec: corev1.ServiceSpec{
					Ports:    []corev1.ServicePort{{Name: "http", Port: 8000, Protocol: corev1.ProtocolTCP}},
					Selector: map[string]string{"app.kubernetes.io/name": agentName},
				},
			}
			Expect(k8sClient.Create(ctx, service)).To(Succeed())

			By("creating a signed agent card (JWS format)")
			cardData := &agentv1alpha1.AgentCardData{
				Name:    "Valid Signed Agent",
				Version: "1.0.0",
				URL:     "http://localhost:8000",
			}
			jwsSig := buildTestJWS(cardData, rsaPrivKey, "my-signing-key", "")
			cardData.Signatures = []agentv1alpha1.AgentCardSignature{jwsSig}

			By("creating an AgentCard CR")
			agentCard := &agentv1alpha1.AgentCard{
				ObjectMeta: metav1.ObjectMeta{Name: agentCardName, Namespace: namespace},
				Spec: agentv1alpha1.AgentCardSpec{
					SyncPeriod: "30s",
					Selector: &agentv1alpha1.AgentSelector{
						MatchLabels: map[string]string{
							"app.kubernetes.io/name": agentName,
							LabelAgentType:           LabelValueAgent,
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, agentCard)).To(Succeed())

			By("configuring a reconciler with signature verification enabled")
			provider, err := signature.NewSecretProvider(&signature.Config{
				Type:            signature.ProviderTypeSecret,
				SecretName:      secretName,
				SecretNamespace: namespace,
			})
			Expect(err).NotTo(HaveOccurred())
			provider.(*signature.SecretProvider).SetClient(k8sClient)

			reconciler := &AgentCardReconciler{
				Client:             k8sClient,
				Scheme:             k8sClient.Scheme(),
				AgentFetcher:       &mockFetcher{cardData: cardData},
				RequireSignature:   true,
				SignatureProvider:  provider,
				SignatureAuditMode: false,
			}

			// First reconcile adds finalizer
			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: agentCardName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			// Second reconcile performs verification
			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: agentCardName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			By("verifying validSignature=true")
			Eventually(func() bool {
				card := &agentv1alpha1.AgentCard{}
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: agentCardName, Namespace: namespace}, card); err != nil {
					return false
				}
				return card.Status.ValidSignature != nil && *card.Status.ValidSignature
			}, timeout, interval).Should(BeTrue())

			By("verifying SignatureVerified condition is True")
			card := &agentv1alpha1.AgentCard{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: agentCardName, Namespace: namespace}, card)).To(Succeed())
			sigCond := findCondition(card.Status.Conditions, "SignatureVerified")
			Expect(sigCond).NotTo(BeNil())
			Expect(sigCond.Status).To(Equal(metav1.ConditionTrue))
			Expect(sigCond.Reason).To(Equal(ReasonSignatureValid))

			By("verifying signatureKeyId is set")
			Expect(card.Status.SignatureKeyID).To(Equal("my-signing-key"))

			By("verifying Synced condition is True")
			syncedCond := findCondition(card.Status.Conditions, "Synced")
			Expect(syncedCond).NotTo(BeNil())
			Expect(syncedCond.Status).To(Equal(metav1.ConditionTrue))
		})
	})

	Context("Unsigned AgentCard — Rejected", func() {
		const (
			agentName     = "sig-unsigned-agent"
			agentCardName = "sig-unsigned-card"
			namespace     = "default"
			secretName    = "sig-unsigned-keys"
		)

		ctx := context.Background()

		BeforeEach(func() {
			By("creating a public key Secret")
			_, pubPEM := generateTestRSAKeyPair()
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: namespace},
				Data:       map[string][]byte{"key": pubPEM},
			}
			Expect(k8sClient.Create(ctx, secret)).To(Succeed())
		})

		AfterEach(func() {
			cleanupResource(ctx, &agentv1alpha1.AgentCard{}, agentCardName, namespace)
			cleanupResource(ctx, &agentv1alpha1.Agent{}, agentName, namespace)
			cleanupResource(ctx, &corev1.Service{}, agentName, namespace)
			cleanupResource(ctx, &corev1.Secret{}, secretName, namespace)
		})

		It("should set validSignature=false for an unsigned card", func() {
			By("creating Agent, Service, and AgentCard")
			createAgentWithService(ctx, agentName, namespace)

			agentCard := &agentv1alpha1.AgentCard{
				ObjectMeta: metav1.ObjectMeta{Name: agentCardName, Namespace: namespace},
				Spec: agentv1alpha1.AgentCardSpec{
					SyncPeriod: "30s",
					Selector: &agentv1alpha1.AgentSelector{
						MatchLabels: map[string]string{
							"app.kubernetes.io/name": agentName,
							LabelAgentType:           LabelValueAgent,
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, agentCard)).To(Succeed())

			By("setting up reconciler with unsigned card data")
			cardData := &agentv1alpha1.AgentCardData{
				Name:    "Unsigned Agent",
				Version: "1.0.0",
				URL:     "http://localhost:8000",
				// No Signatures field
			}

			provider, err := signature.NewSecretProvider(&signature.Config{
				Type:            signature.ProviderTypeSecret,
				SecretName:      secretName,
				SecretNamespace: namespace,
			})
			Expect(err).NotTo(HaveOccurred())
			provider.(*signature.SecretProvider).SetClient(k8sClient)

			reconciler := &AgentCardReconciler{
				Client:             k8sClient,
				Scheme:             k8sClient.Scheme(),
				AgentFetcher:       &mockFetcher{cardData: cardData},
				RequireSignature:   true,
				SignatureProvider:  provider,
				SignatureAuditMode: false,
			}

			// Reconcile twice (finalizer + verify)
			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: agentCardName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())
			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: agentCardName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			By("verifying validSignature=false")
			Eventually(func() bool {
				card := &agentv1alpha1.AgentCard{}
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: agentCardName, Namespace: namespace}, card); err != nil {
					return false
				}
				return card.Status.ValidSignature != nil && !*card.Status.ValidSignature
			}, timeout, interval).Should(BeTrue())

			By("verifying SignatureVerified condition is False")
			card := &agentv1alpha1.AgentCard{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: agentCardName, Namespace: namespace}, card)).To(Succeed())
			sigCond := findCondition(card.Status.Conditions, "SignatureVerified")
			Expect(sigCond).NotTo(BeNil())
			Expect(sigCond.Status).To(Equal(metav1.ConditionFalse))
			Expect(sigCond.Reason).To(Equal(ReasonSignatureInvalid))
		})
	})

	Context("Wrong-Key JWS Signature — Rejected", func() {
		const (
			agentName     = "sig-wrongkey-agent"
			agentCardName = "sig-wrongkey-card"
			namespace     = "default"
			secretName    = "sig-wrongkey-keys"
		)

		ctx := context.Background()

		AfterEach(func() {
			cleanupResource(ctx, &agentv1alpha1.AgentCard{}, agentCardName, namespace)
			cleanupResource(ctx, &agentv1alpha1.Agent{}, agentName, namespace)
			cleanupResource(ctx, &corev1.Service{}, agentName, namespace)
			cleanupResource(ctx, &corev1.Secret{}, secretName, namespace)
		})

		It("should set validSignature=false when card is signed with wrong key", func() {
			By("generating two different key pairs")
			signingKey, _ := generateTestRSAKeyPair()
			_, wrongPubPEM := generateTestRSAKeyPair()

			By("creating secret with the wrong public key")
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: namespace},
				Data:       map[string][]byte{"key-1": wrongPubPEM},
			}
			Expect(k8sClient.Create(ctx, secret)).To(Succeed())

			By("creating Agent, Service, and AgentCard")
			createAgentWithService(ctx, agentName, namespace)

			cardData := &agentv1alpha1.AgentCardData{
				Name:    "Wrong Key Agent",
				Version: "1.0.0",
				URL:     "http://localhost:8000",
			}
			jwsSig := buildTestJWS(cardData, signingKey, "key-1", "")
			cardData.Signatures = []agentv1alpha1.AgentCardSignature{jwsSig}

			agentCard := &agentv1alpha1.AgentCard{
				ObjectMeta: metav1.ObjectMeta{Name: agentCardName, Namespace: namespace},
				Spec: agentv1alpha1.AgentCardSpec{
					SyncPeriod: "30s",
					Selector: &agentv1alpha1.AgentSelector{
						MatchLabels: map[string]string{
							"app.kubernetes.io/name": agentName,
							LabelAgentType:           LabelValueAgent,
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, agentCard)).To(Succeed())

			By("reconciling with signature verification")
			provider, err := signature.NewSecretProvider(&signature.Config{
				Type:            signature.ProviderTypeSecret,
				SecretName:      secretName,
				SecretNamespace: namespace,
			})
			Expect(err).NotTo(HaveOccurred())
			provider.(*signature.SecretProvider).SetClient(k8sClient)

			reconciler := &AgentCardReconciler{
				Client:             k8sClient,
				Scheme:             k8sClient.Scheme(),
				AgentFetcher:       &mockFetcher{cardData: cardData},
				RequireSignature:   true,
				SignatureProvider:  provider,
				SignatureAuditMode: false,
			}

			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: agentCardName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())
			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: agentCardName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			By("verifying validSignature=false")
			card := &agentv1alpha1.AgentCard{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: agentCardName, Namespace: namespace}, card)).To(Succeed())
			Expect(card.Status.ValidSignature).NotTo(BeNil())
			Expect(*card.Status.ValidSignature).To(BeFalse())

			By("verifying Synced condition is False with InvalidSignature reason")
			syncedCond := findCondition(card.Status.Conditions, "Synced")
			Expect(syncedCond).NotTo(BeNil())
			Expect(syncedCond.Status).To(Equal(metav1.ConditionFalse))
			Expect(syncedCond.Reason).To(Equal(ReasonSignatureInvalid))
		})
	})

	Context("Audit Mode — Accept with Warning", func() {
		const (
			agentName     = "sig-audit-agent"
			agentCardName = "sig-audit-card"
			namespace     = "default"
			secretName    = "sig-audit-keys"
		)

		ctx := context.Background()

		AfterEach(func() {
			cleanupResource(ctx, &agentv1alpha1.AgentCard{}, agentCardName, namespace)
			cleanupResource(ctx, &agentv1alpha1.Agent{}, agentName, namespace)
			cleanupResource(ctx, &corev1.Service{}, agentName, namespace)
			cleanupResource(ctx, &corev1.Secret{}, secretName, namespace)
		})

		It("should allow unsigned card in audit mode and set Synced=True", func() {
			_, pubPEM := generateTestRSAKeyPair()

			By("creating secret")
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: namespace},
				Data:       map[string][]byte{"key": pubPEM},
			}
			Expect(k8sClient.Create(ctx, secret)).To(Succeed())

			By("creating Agent, Service, and AgentCard")
			createAgentWithService(ctx, agentName, namespace)

			cardData := &agentv1alpha1.AgentCardData{
				Name:    "Audit Agent",
				Version: "1.0.0",
				URL:     "http://localhost:8000",
				// No Signatures
			}

			agentCard := &agentv1alpha1.AgentCard{
				ObjectMeta: metav1.ObjectMeta{Name: agentCardName, Namespace: namespace},
				Spec: agentv1alpha1.AgentCardSpec{
					SyncPeriod: "30s",
					Selector: &agentv1alpha1.AgentSelector{
						MatchLabels: map[string]string{
							"app.kubernetes.io/name": agentName,
							LabelAgentType:           LabelValueAgent,
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, agentCard)).To(Succeed())

			By("reconciling with audit mode enabled")
			provider, err := signature.NewSecretProvider(&signature.Config{
				Type:            signature.ProviderTypeSecret,
				SecretName:      secretName,
				SecretNamespace: namespace,
				AuditMode:       true,
			})
			Expect(err).NotTo(HaveOccurred())
			provider.(*signature.SecretProvider).SetClient(k8sClient)

			reconciler := &AgentCardReconciler{
				Client:             k8sClient,
				Scheme:             k8sClient.Scheme(),
				AgentFetcher:       &mockFetcher{cardData: cardData},
				RequireSignature:   true,
				SignatureProvider:  provider,
				SignatureAuditMode: true,
			}

			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: agentCardName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())
			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: agentCardName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			By("verifying card is synced (audit mode allows it)")
			Eventually(func() bool {
				card := &agentv1alpha1.AgentCard{}
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: agentCardName, Namespace: namespace}, card); err != nil {
					return false
				}
				syncedCond := findCondition(card.Status.Conditions, "Synced")
				return syncedCond != nil && syncedCond.Status == metav1.ConditionTrue
			}, timeout, interval).Should(BeTrue())

			By("verifying SignatureVerified condition mentions audit mode")
			card := &agentv1alpha1.AgentCard{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: agentCardName, Namespace: namespace}, card)).To(Succeed())
			sigCond := findCondition(card.Status.Conditions, "SignatureVerified")
			Expect(sigCond).NotTo(BeNil())
			// In audit mode, unsigned cards pass via the provider (audit mode returns verified=true)
			Expect(sigCond.Status).To(Equal(metav1.ConditionTrue))
		})
	})

	Context("No Signature Required — Verification Skipped", func() {
		const (
			agentName     = "sig-none-agent"
			agentCardName = "sig-none-card"
			namespace     = "default"
		)

		ctx := context.Background()

		AfterEach(func() {
			cleanupResource(ctx, &agentv1alpha1.AgentCard{}, agentCardName, namespace)
			cleanupResource(ctx, &agentv1alpha1.Agent{}, agentName, namespace)
			cleanupResource(ctx, &corev1.Service{}, agentName, namespace)
		})

		It("should sync card without checking signature when RequireSignature=false", func() {
			By("creating Agent, Service, and AgentCard")
			createAgentWithService(ctx, agentName, namespace)

			cardData := &agentv1alpha1.AgentCardData{
				Name:    "No Sig Agent",
				Version: "1.0.0",
				URL:     "http://localhost:8000",
			}

			agentCard := &agentv1alpha1.AgentCard{
				ObjectMeta: metav1.ObjectMeta{Name: agentCardName, Namespace: namespace},
				Spec: agentv1alpha1.AgentCardSpec{
					SyncPeriod: "30s",
					Selector: &agentv1alpha1.AgentSelector{
						MatchLabels: map[string]string{
							"app.kubernetes.io/name": agentName,
							LabelAgentType:           LabelValueAgent,
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, agentCard)).To(Succeed())

			By("reconciling WITHOUT signature verification")
			reconciler := &AgentCardReconciler{
				Client:           k8sClient,
				Scheme:           k8sClient.Scheme(),
				AgentFetcher:     &mockFetcher{cardData: cardData},
				RequireSignature: false,
			}

			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: agentCardName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())
			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: agentCardName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			By("verifying card is synced and validSignature is nil (not evaluated)")
			card := &agentv1alpha1.AgentCard{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: agentCardName, Namespace: namespace}, card)).To(Succeed())
			Expect(card.Status.ValidSignature).To(BeNil())

			syncedCond := findCondition(card.Status.Conditions, "Synced")
			Expect(syncedCond).NotTo(BeNil())
			Expect(syncedCond.Status).To(Equal(metav1.ConditionTrue))
		})
	})

	Context("Signature Identity Match", func() {
		const (
			agentName     = "sig-identity-agent"
			agentCardName = "sig-identity-card"
			namespace     = "default"
			secretName    = "sig-identity-keys"
			trustDomain   = "test.local"
		)

		ctx := context.Background()

		AfterEach(func() {
			cleanupResource(ctx, &agentv1alpha1.AgentCard{}, agentCardName, namespace)
			cleanupResource(ctx, &agentv1alpha1.Agent{}, agentName, namespace)
			cleanupResource(ctx, &corev1.Service{}, agentName, namespace)
			cleanupResource(ctx, &corev1.Secret{}, secretName, namespace)
		})

		It("should set signatureIdentityMatch=true when both signature and binding pass", func() {
			By("generating key pair")
			privKey, pubPEM := generateTestRSAKeyPair()

			By("creating secret")
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: namespace},
				Data:       map[string][]byte{"key-1": pubPEM},
			}
			Expect(k8sClient.Create(ctx, secret)).To(Succeed())

			By("creating Agent with service account")
			agent := &agentv1alpha1.Agent{
				ObjectMeta: metav1.ObjectMeta{
					Name:      agentName,
					Namespace: namespace,
					Labels: map[string]string{
						"app.kubernetes.io/name": agentName,
						LabelAgentType:           LabelValueAgent,
						LabelAgentProtocol:       "a2a",
					},
				},
				Spec: agentv1alpha1.AgentSpec{
					PodTemplateSpec: &corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							ServiceAccountName: "test-sa",
							Containers: []corev1.Container{
								{Name: "agent", Image: "test-image:latest"},
							},
						},
					},
					ImageSource: agentv1alpha1.ImageSource{Image: ptr.To("test-image:latest")},
				},
			}
			Expect(k8sClient.Create(ctx, agent)).To(Succeed())
			Eventually(func() error {
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: agentName, Namespace: namespace}, agent); err != nil {
					return err
				}
				agent.Status.DeploymentStatus = &agentv1alpha1.DeploymentStatus{Phase: agentv1alpha1.PhaseReady}
				return k8sClient.Status().Update(ctx, agent)
			}).Should(Succeed())

			By("creating a Service")
			service := &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{Name: agentName, Namespace: namespace},
				Spec: corev1.ServiceSpec{
					Ports:    []corev1.ServicePort{{Name: "http", Port: 8000, Protocol: corev1.ProtocolTCP}},
					Selector: map[string]string{"app.kubernetes.io/name": agentName},
				},
			}
			Expect(k8sClient.Create(ctx, service)).To(Succeed())

			By("creating signed card data (JWS format)")
			cardData := &agentv1alpha1.AgentCardData{
				Name:    "Identity Agent",
				Version: "1.0.0",
				URL:     "http://localhost:8000",
			}
			jwsSig := buildTestJWS(cardData, privKey, "key-1", "")
			cardData.Signatures = []agentv1alpha1.AgentCardSignature{jwsSig}

			By("creating AgentCard with both signature verification and identity binding")
			expectedSpiffeID := "spiffe://" + trustDomain + "/ns/" + namespace + "/sa/test-sa"
			agentCard := &agentv1alpha1.AgentCard{
				ObjectMeta: metav1.ObjectMeta{Name: agentCardName, Namespace: namespace},
				Spec: agentv1alpha1.AgentCardSpec{
					SyncPeriod: "30s",
					Selector: &agentv1alpha1.AgentSelector{
						MatchLabels: map[string]string{
							"app.kubernetes.io/name": agentName,
							LabelAgentType:           LabelValueAgent,
						},
					},
					IdentityBinding: &agentv1alpha1.IdentityBinding{
						TrustDomain:      trustDomain,
						AllowedSpiffeIDs: []agentv1alpha1.SpiffeID{agentv1alpha1.SpiffeID(expectedSpiffeID)},
					},
				},
			}
			Expect(k8sClient.Create(ctx, agentCard)).To(Succeed())

			By("reconciling with both signature and identity binding")
			provider, err := signature.NewSecretProvider(&signature.Config{
				Type:            signature.ProviderTypeSecret,
				SecretName:      secretName,
				SecretNamespace: namespace,
			})
			Expect(err).NotTo(HaveOccurred())
			provider.(*signature.SecretProvider).SetClient(k8sClient)

			reconciler := &AgentCardReconciler{
				Client:             k8sClient,
				Scheme:             k8sClient.Scheme(),
				AgentFetcher:       &mockFetcher{cardData: cardData},
				RequireSignature:   true,
				SignatureProvider:  provider,
				SignatureAuditMode: false,
				TrustDomain:        trustDomain,
			}

			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: agentCardName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())
			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: agentCardName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			By("verifying signatureIdentityMatch=true")
			Eventually(func() bool {
				card := &agentv1alpha1.AgentCard{}
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: agentCardName, Namespace: namespace}, card); err != nil {
					return false
				}
				return card.Status.SignatureIdentityMatch != nil && *card.Status.SignatureIdentityMatch
			}, timeout, interval).Should(BeTrue())
		})
	})

	Context("Spoofed JWS spiffe_id - should reject when JWS identity doesn't match workload", func() {
		const (
			agentName     = "sig-spoof-agent"
			agentCardName = "sig-spoof-card"
			namespace     = "default"
			secretName    = "sig-spoof-keys"
			trustDomain   = "test.local"
		)

		ctx := context.Background()

		AfterEach(func() {
			cleanupResource(ctx, &agentv1alpha1.AgentCard{}, agentCardName, namespace)
			cleanupResource(ctx, &agentv1alpha1.Agent{}, agentName, namespace)
			cleanupResource(ctx, &corev1.Service{}, agentName, namespace)
			cleanupResource(ctx, &corev1.Secret{}, secretName, namespace)
		})

		It("should set signatureIdentityMatch=false when JWS spiffe_id doesn't match workload SA", func() {
			By("generating key pair")
			privKey, pubPEM := generateTestRSAKeyPair()

			By("creating secret with the signing public key")
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: namespace},
				Data:       map[string][]byte{"key-1": pubPEM},
			}
			Expect(k8sClient.Create(ctx, secret)).To(Succeed())

			By("creating Agent with service account 'real-sa'")
			agent := &agentv1alpha1.Agent{
				ObjectMeta: metav1.ObjectMeta{
					Name:      agentName,
					Namespace: namespace,
					Labels: map[string]string{
						"app.kubernetes.io/name": agentName,
						LabelAgentType:           LabelValueAgent,
						LabelAgentProtocol:       "a2a",
					},
				},
				Spec: agentv1alpha1.AgentSpec{
					PodTemplateSpec: &corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							ServiceAccountName: "real-sa",
							Containers: []corev1.Container{
								{Name: "agent", Image: "test-image:latest"},
							},
						},
					},
					ImageSource: agentv1alpha1.ImageSource{Image: ptr.To("test-image:latest")},
				},
			}
			Expect(k8sClient.Create(ctx, agent)).To(Succeed())
			Eventually(func() error {
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: agentName, Namespace: namespace}, agent); err != nil {
					return err
				}
				agent.Status.DeploymentStatus = &agentv1alpha1.DeploymentStatus{Phase: agentv1alpha1.PhaseReady}
				return k8sClient.Status().Update(ctx, agent)
			}).Should(Succeed())

			By("creating a Service")
			service := &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{Name: agentName, Namespace: namespace},
				Spec: corev1.ServiceSpec{
					Ports:    []corev1.ServicePort{{Name: "http", Port: 8000, Protocol: corev1.ProtocolTCP}},
					Selector: map[string]string{"app.kubernetes.io/name": agentName},
				},
			}
			Expect(k8sClient.Create(ctx, service)).To(Succeed())

			By("creating signed card with SPOOFED spiffe_id (claims 'impostor-sa' but workload is 'real-sa')")
			// The attacker signs the card asserting spiffe_id=impostor-sa.
			// The workload actually runs as real-sa.
			// Both identities are in the allowlist, so a naive check passes either one.
			// But a cross-check must detect the mismatch and reject.
			impostorSpiffeID := "spiffe://" + trustDomain + "/ns/" + namespace + "/sa/impostor-sa"
			realSpiffeID := "spiffe://" + trustDomain + "/ns/" + namespace + "/sa/real-sa"
			cardData := &agentv1alpha1.AgentCardData{
				Name:    "Spoofed Identity Agent",
				Version: "1.0.0",
				URL:     "http://localhost:8000",
			}
			jwsSig := buildTestJWS(cardData, privKey, "key-1", impostorSpiffeID)
			cardData.Signatures = []agentv1alpha1.AgentCardSignature{jwsSig}

			By("creating AgentCard — allowlist contains BOTH identities (real and impostor)")
			agentCard := &agentv1alpha1.AgentCard{
				ObjectMeta: metav1.ObjectMeta{Name: agentCardName, Namespace: namespace},
				Spec: agentv1alpha1.AgentCardSpec{
					SyncPeriod: "30s",
					Selector: &agentv1alpha1.AgentSelector{
						MatchLabels: map[string]string{
							"app.kubernetes.io/name": agentName,
							LabelAgentType:           LabelValueAgent,
						},
					},
					IdentityBinding: &agentv1alpha1.IdentityBinding{
						TrustDomain: trustDomain,
						// Both are in the allowlist — the naive check passes either one.
						// Only a cross-check (JWS identity == workload identity) catches this.
						AllowedSpiffeIDs: []agentv1alpha1.SpiffeID{
							agentv1alpha1.SpiffeID(realSpiffeID),
							agentv1alpha1.SpiffeID(impostorSpiffeID),
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, agentCard)).To(Succeed())

			By("reconciling with signature verification + identity binding")
			provider, err := signature.NewSecretProvider(&signature.Config{
				Type:            signature.ProviderTypeSecret,
				SecretName:      secretName,
				SecretNamespace: namespace,
			})
			Expect(err).NotTo(HaveOccurred())
			provider.(*signature.SecretProvider).SetClient(k8sClient)

			reconciler := &AgentCardReconciler{
				Client:             k8sClient,
				Scheme:             k8sClient.Scheme(),
				AgentFetcher:       &mockFetcher{cardData: cardData},
				RequireSignature:   true,
				SignatureProvider:  provider,
				SignatureAuditMode: false,
				TrustDomain:        trustDomain,
			}

			// Reconcile: first adds finalizer
			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: agentCardName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			// Second reconcile: verifies signature + evaluates binding
			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: agentCardName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			By("verifying signatureIdentityMatch is false — JWS identity vs workload identity mismatch")
			// The JWS claims spiffe_id=impostor-sa, workload runs as real-sa.
			// Both are in the allowlist, so a naive allowlist-only check passes.
			// A correct implementation cross-checks JWS identity against workload
			// identity and rejects the mismatch.
			card := &agentv1alpha1.AgentCard{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: agentCardName, Namespace: namespace}, card)).To(Succeed())
			Expect(card.Status.SignatureIdentityMatch).NotTo(BeNil(),
				"SignatureIdentityMatch should be set after reconcile with signature verification + binding")
			Expect(*card.Status.SignatureIdentityMatch).To(BeFalse(),
				"JWS spiffe_id (impostor-sa) does not match workload SA (real-sa) — cross-check should reject")
		})
	})

	Context("Label Propagation — Valid Signature with targetRef Deployment", func() {
		const (
			deploymentName = "sig-label-agent"
			agentCardName  = "sig-label-card"
			namespace      = "default"
			secretName     = "sig-label-keys"
		)

		ctx := context.Background()

		AfterEach(func() {
			cleanupResource(ctx, &agentv1alpha1.AgentCard{}, agentCardName, namespace)
			cleanupResource(ctx, &appsv1.Deployment{}, deploymentName, namespace)
			cleanupResource(ctx, &corev1.Service{}, deploymentName, namespace)
			cleanupResource(ctx, &corev1.Secret{}, secretName, namespace)
		})

		It("should propagate signature-verified=true label to Deployment pod template on valid signature", func() {
			By("generating key pair and creating secret")
			privKey, pubPEM := generateTestRSAKeyPair()
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: namespace},
				Data:       map[string][]byte{"key-1": pubPEM},
			}
			Expect(k8sClient.Create(ctx, secret)).To(Succeed())

			By("creating a Deployment directly (not via Agent CRD)")
			replicas := int32(1)
			deployment := &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      deploymentName,
					Namespace: namespace,
					Labels: map[string]string{
						"app":                deploymentName,
						LabelAgentType:       LabelValueAgent,
						LabelKagentiProtocol: "a2a",
					},
				},
				Spec: appsv1.DeploymentSpec{
					Replicas: &replicas,
					Selector: &metav1.LabelSelector{
						MatchLabels: map[string]string{"app": deploymentName},
					},
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Labels: map[string]string{"app": deploymentName},
						},
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{Name: "agent", Image: "test-image:latest"},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, deployment)).To(Succeed())

			By("marking the Deployment as available (simulating real controller)")
			Eventually(func() error {
				d := &appsv1.Deployment{}
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: deploymentName, Namespace: namespace}, d); err != nil {
					return err
				}
				d.Status.Conditions = []appsv1.DeploymentCondition{
					{
						Type:   appsv1.DeploymentAvailable,
						Status: corev1.ConditionTrue,
					},
				}
				d.Status.Replicas = 1
				d.Status.ReadyReplicas = 1
				return k8sClient.Status().Update(ctx, d)
			}).Should(Succeed())

			By("creating a Service for the Deployment")
			service := &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{Name: deploymentName, Namespace: namespace},
				Spec: corev1.ServiceSpec{
					Ports:    []corev1.ServicePort{{Name: "http", Port: 8000, Protocol: corev1.ProtocolTCP}},
					Selector: map[string]string{"app": deploymentName},
				},
			}
			Expect(k8sClient.Create(ctx, service)).To(Succeed())

			By("creating signed card data (JWS format)")
			cardData := &agentv1alpha1.AgentCardData{
				Name:    "Label Test Agent",
				Version: "1.0.0",
				URL:     "http://localhost:8000",
			}
			jwsSig := buildTestJWS(cardData, privKey, "key-1", "")
			cardData.Signatures = []agentv1alpha1.AgentCardSignature{jwsSig}

			By("creating AgentCard with targetRef pointing to the Deployment")
			agentCard := &agentv1alpha1.AgentCard{
				ObjectMeta: metav1.ObjectMeta{Name: agentCardName, Namespace: namespace},
				Spec: agentv1alpha1.AgentCardSpec{
					SyncPeriod: "30s",
					TargetRef: &agentv1alpha1.TargetRef{
						APIVersion: "apps/v1",
						Kind:       "Deployment",
						Name:       deploymentName,
					},
				},
			}
			Expect(k8sClient.Create(ctx, agentCard)).To(Succeed())

			By("reconciling with signature verification enabled")
			provider, err := signature.NewSecretProvider(&signature.Config{
				Type:            signature.ProviderTypeSecret,
				SecretName:      secretName,
				SecretNamespace: namespace,
			})
			Expect(err).NotTo(HaveOccurred())
			provider.(*signature.SecretProvider).SetClient(k8sClient)

			reconciler := &AgentCardReconciler{
				Client:             k8sClient,
				Scheme:             k8sClient.Scheme(),
				AgentFetcher:       &mockFetcher{cardData: cardData},
				RequireSignature:   true,
				SignatureProvider:  provider,
				SignatureAuditMode: false,
			}

			// First reconcile adds finalizer
			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: agentCardName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			// Second reconcile performs verification + label propagation
			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: agentCardName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			By("verifying the signature-verified label is set on the Deployment pod template")
			Eventually(func() string {
				d := &appsv1.Deployment{}
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: deploymentName, Namespace: namespace}, d); err != nil {
					return ""
				}
				return d.Spec.Template.Labels[LabelSignatureVerified]
			}, timeout, interval).Should(Equal("true"))
		})
	})

	Context("Label Propagation — Invalid Signature removes label from Deployment", func() {
		const (
			deploymentName = "sig-label-rm-agent"
			agentCardName  = "sig-label-rm-card"
			namespace      = "default"
			secretName     = "sig-label-rm-keys"
		)

		ctx := context.Background()

		AfterEach(func() {
			cleanupResource(ctx, &agentv1alpha1.AgentCard{}, agentCardName, namespace)
			cleanupResource(ctx, &appsv1.Deployment{}, deploymentName, namespace)
			cleanupResource(ctx, &corev1.Service{}, deploymentName, namespace)
			cleanupResource(ctx, &corev1.Secret{}, secretName, namespace)
		})

		It("should remove signature-verified label when signature becomes invalid", func() {
			By("generating two key pairs — signing key and wrong verification key")
			signingKey, _ := generateTestRSAKeyPair()
			_, wrongPubPEM := generateTestRSAKeyPair()

			By("creating secret with the wrong public key")
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: namespace},
				Data:       map[string][]byte{"key-1": wrongPubPEM},
			}
			Expect(k8sClient.Create(ctx, secret)).To(Succeed())

			By("creating a Deployment with the signature-verified label already set (simulating previous valid state)")
			replicas := int32(1)
			deployment := &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      deploymentName,
					Namespace: namespace,
					Labels: map[string]string{
						"app":                deploymentName,
						LabelAgentType:       LabelValueAgent,
						LabelKagentiProtocol: "a2a",
					},
				},
				Spec: appsv1.DeploymentSpec{
					Replicas: &replicas,
					Selector: &metav1.LabelSelector{
						MatchLabels: map[string]string{"app": deploymentName},
					},
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Labels: map[string]string{
								"app":                  deploymentName,
								LabelSignatureVerified: "true", // pre-existing from previous valid state
							},
						},
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{Name: "agent", Image: "test-image:latest"},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, deployment)).To(Succeed())

			By("marking the Deployment as available")
			Eventually(func() error {
				d := &appsv1.Deployment{}
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: deploymentName, Namespace: namespace}, d); err != nil {
					return err
				}
				d.Status.Conditions = []appsv1.DeploymentCondition{
					{
						Type:   appsv1.DeploymentAvailable,
						Status: corev1.ConditionTrue,
					},
				}
				d.Status.Replicas = 1
				d.Status.ReadyReplicas = 1
				return k8sClient.Status().Update(ctx, d)
			}).Should(Succeed())

			By("verifying label is initially present")
			d := &appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: deploymentName, Namespace: namespace}, d)).To(Succeed())
			Expect(d.Spec.Template.Labels[LabelSignatureVerified]).To(Equal("true"))

			By("creating a Service for the Deployment")
			service := &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{Name: deploymentName, Namespace: namespace},
				Spec: corev1.ServiceSpec{
					Ports:    []corev1.ServicePort{{Name: "http", Port: 8000, Protocol: corev1.ProtocolTCP}},
					Selector: map[string]string{"app": deploymentName},
				},
			}
			Expect(k8sClient.Create(ctx, service)).To(Succeed())

			By("creating card signed with wrong key (JWS format)")
			cardData := &agentv1alpha1.AgentCardData{
				Name:    "Label Removal Agent",
				Version: "1.0.0",
				URL:     "http://localhost:8000",
			}
			jwsSig := buildTestJWS(cardData, signingKey, "key-1", "")
			cardData.Signatures = []agentv1alpha1.AgentCardSignature{jwsSig}

			By("creating AgentCard with targetRef")
			agentCard := &agentv1alpha1.AgentCard{
				ObjectMeta: metav1.ObjectMeta{Name: agentCardName, Namespace: namespace},
				Spec: agentv1alpha1.AgentCardSpec{
					SyncPeriod: "30s",
					TargetRef: &agentv1alpha1.TargetRef{
						APIVersion: "apps/v1",
						Kind:       "Deployment",
						Name:       deploymentName,
					},
				},
			}
			Expect(k8sClient.Create(ctx, agentCard)).To(Succeed())

			By("reconciling with signature verification enabled")
			provider, err := signature.NewSecretProvider(&signature.Config{
				Type:            signature.ProviderTypeSecret,
				SecretName:      secretName,
				SecretNamespace: namespace,
			})
			Expect(err).NotTo(HaveOccurred())
			provider.(*signature.SecretProvider).SetClient(k8sClient)

			reconciler := &AgentCardReconciler{
				Client:             k8sClient,
				Scheme:             k8sClient.Scheme(),
				AgentFetcher:       &mockFetcher{cardData: cardData},
				RequireSignature:   true,
				SignatureProvider:  provider,
				SignatureAuditMode: false,
			}

			// First reconcile adds finalizer
			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: agentCardName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			// Second reconcile performs verification — fails — should remove label
			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: agentCardName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			By("verifying the signature-verified label was REMOVED from the Deployment pod template")
			Eventually(func() string {
				d := &appsv1.Deployment{}
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: deploymentName, Namespace: namespace}, d); err != nil {
					return "error"
				}
				return d.Spec.Template.Labels[LabelSignatureVerified]
			}, timeout, interval).Should(BeEmpty())

			By("verifying validSignature=false")
			card := &agentv1alpha1.AgentCard{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: agentCardName, Namespace: namespace}, card)).To(Succeed())
			Expect(card.Status.ValidSignature).NotTo(BeNil())
			Expect(*card.Status.ValidSignature).To(BeFalse())
		})
	})
})

// --- Test helpers ---

// generateTestRSAKeyPair generates an RSA key pair for testing.
func generateTestRSAKeyPair() (*rsa.PrivateKey, []byte) {
	privKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	pubDER, _ := x509.MarshalPKIXPublicKey(&privKey.PublicKey)
	pubPEM := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER})
	return privKey, pubPEM
}

// buildTestJWS creates a JWS signature for integration testing.
// Builds a protected header with alg, kid, and optional spiffe_id,
// then signs the canonical card payload per A2A spec JWS format.
func buildTestJWS(cardData *agentv1alpha1.AgentCardData, privKey *rsa.PrivateKey, kid, spiffeID string) agentv1alpha1.AgentCardSignature {
	// Build protected header (per A2A spec §8.4.2: alg, typ, kid are MUST)
	header := map[string]string{"alg": "RS256", "typ": "JOSE", "kid": kid}
	if spiffeID != "" {
		header["spiffe_id"] = spiffeID
	}
	headerJSON, _ := json.Marshal(header)
	protectedB64 := base64.RawURLEncoding.EncodeToString(headerJSON)

	// Create canonical payload
	rawJSON, _ := json.Marshal(cardData)
	var cardMap map[string]interface{}
	json.Unmarshal(rawJSON, &cardMap)
	delete(cardMap, "signatures")
	cleanMap := removeEmptyFieldsTest(cardMap)
	payload, _ := marshalCanonicalTest(cleanMap)

	// Construct signing input
	payloadB64 := base64.RawURLEncoding.EncodeToString(payload)
	signingInput := []byte(protectedB64 + "." + payloadB64)

	// Sign
	hash := sha256.Sum256(signingInput)
	sig, _ := rsa.SignPKCS1v15(rand.Reader, privKey, crypto.SHA256, hash[:])

	return agentv1alpha1.AgentCardSignature{
		Protected: protectedB64,
		Signature: base64.RawURLEncoding.EncodeToString(sig),
	}
}

// createAgentWithService creates a minimal Agent and Service for testing.
func createAgentWithService(ctx context.Context, agentName, namespace string) {
	agent := &agentv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      agentName,
			Namespace: namespace,
			Labels: map[string]string{
				"app.kubernetes.io/name": agentName,
				LabelAgentType:           LabelValueAgent,
				LabelAgentProtocol:       "a2a",
			},
		},
		Spec: agentv1alpha1.AgentSpec{
			PodTemplateSpec: &corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{Name: "agent", Image: "test-image:latest"},
					},
				},
			},
			ImageSource: agentv1alpha1.ImageSource{Image: ptr.To("test-image:latest")},
		},
	}
	ExpectWithOffset(1, k8sClient.Create(ctx, agent)).To(Succeed())

	Eventually(func() error {
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: agentName, Namespace: namespace}, agent); err != nil {
			return err
		}
		agent.Status.DeploymentStatus = &agentv1alpha1.DeploymentStatus{Phase: agentv1alpha1.PhaseReady}
		return k8sClient.Status().Update(ctx, agent)
	}).Should(Succeed())

	service := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: agentName, Namespace: namespace},
		Spec: corev1.ServiceSpec{
			Ports:    []corev1.ServicePort{{Name: "http", Port: 8000, Protocol: corev1.ProtocolTCP}},
			Selector: map[string]string{"app.kubernetes.io/name": agentName},
		},
	}
	ExpectWithOffset(1, k8sClient.Create(ctx, service)).To(Succeed())
}

// removeEmptyFieldsTest mirrors the verifier's removeEmptyFields for test signing.
func removeEmptyFieldsTest(m map[string]interface{}) map[string]interface{} {
	result := make(map[string]interface{})
	for k, v := range m {
		if v == nil {
			continue
		}
		switch val := v.(type) {
		case map[string]interface{}:
			cleaned := removeEmptyFieldsTest(val)
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

// marshalCanonicalTest mirrors the verifier's marshalCanonical for test signing.
func marshalCanonicalTest(data map[string]interface{}) ([]byte, error) {
	return json.Marshal(toSortedMap(data))
}

// toSortedMap produces a deterministic JSON byte slice with sorted keys.
func toSortedMap(m map[string]interface{}) json.RawMessage {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	result := []byte("{")
	for i, k := range keys {
		if i > 0 {
			result = append(result, ',')
		}
		keyJSON, _ := json.Marshal(k)
		result = append(result, keyJSON...)
		result = append(result, ':')

		switch val := m[k].(type) {
		case map[string]interface{}:
			result = append(result, toSortedMap(val)...)
		default:
			valJSON, _ := json.Marshal(val)
			result = append(result, valJSON...)
		}
	}
	result = append(result, '}')
	return result
}
