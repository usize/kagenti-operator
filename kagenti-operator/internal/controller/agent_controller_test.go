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
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	agentv1alpha1 "github.com/kagenti/operator/api/v1alpha1"
	"github.com/kagenti/operator/internal/distribution"
)

var _ = Describe("Agent Controller", func() {
	Context("When reconciling a resource", func() {
		const resourceName = "test-resource"

		ctx := context.Background()

		typeNamespacedName := types.NamespacedName{
			Name:      resourceName,
			Namespace: "default", // TODO(user):Modify as needed
		}
		Agent := &agentv1alpha1.Agent{}

		BeforeEach(func() {
			By("creating the custom resource for the Kind Agent")
			err := k8sClient.Get(ctx, typeNamespacedName, Agent)
			if err != nil && errors.IsNotFound(err) {
				resource := &agentv1alpha1.Agent{
					ObjectMeta: metav1.ObjectMeta{
						Name:      resourceName,
						Namespace: "default",
					},
					Spec: agentv1alpha1.AgentSpec{
						PodTemplateSpec: &corev1.PodTemplateSpec{
							Spec: corev1.PodSpec{
								Containers: []corev1.Container{
									{
										Name:  "agent",
										Image: "test-image:latest",
									},
								},
							},
						},
						ImageSource: agentv1alpha1.ImageSource{
							Image: ptr.To("test-image:latest"),
						},
					},
				}
				Expect(k8sClient.Create(ctx, resource)).To(Succeed())
			}
		})

		AfterEach(func() {
			// TODO(user): Cleanup logic after each test, like removing the resource instance.
			resource := &agentv1alpha1.Agent{}
			err := k8sClient.Get(ctx, typeNamespacedName, resource)
			Expect(err).NotTo(HaveOccurred())

			By("Cleanup the specific resource instance Agent")
			Expect(k8sClient.Delete(ctx, resource)).To(Succeed())
		})
		It("should successfully reconcile the resource", func() {
			By("Reconciling the created resource")
			controllerReconciler := &AgentReconciler{
				Client:       k8sClient,
				Scheme:       k8sClient.Scheme(),
				Distribution: distribution.Kubernetes,
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())
			// TODO(user): Add more specific assertions depending on your controller's reconciliation logic.
			// Example: If you expect a certain status condition after reconciliation, verify it here.
		})
	})
})

func TestSecurityContextByDistribution(t *testing.T) {
	tests := []struct {
		name              string
		distribution      distribution.Type
		expectRunAsUser   bool
		expectFSGroup     bool
		expectedRunAsUser *int64
		expectedFSGroup   *int64
	}{
		{
			name:              "Kubernetes sets UID and GID",
			distribution:      distribution.Kubernetes,
			expectRunAsUser:   true,
			expectFSGroup:     true,
			expectedRunAsUser: ptr.To(int64(1000)),
			expectedFSGroup:   ptr.To(int64(1000)),
		},
		{
			name:              "OpenShift omits UID and GID",
			distribution:      distribution.OpenShift,
			expectRunAsUser:   false,
			expectFSGroup:     false,
			expectedRunAsUser: nil,
			expectedFSGroup:   nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup scheme
			s := runtime.NewScheme()
			_ = scheme.AddToScheme(s)
			_ = agentv1alpha1.AddToScheme(s)

			// Create fake client
			fakeClient := fake.NewClientBuilder().WithScheme(s).Build()

			// Create reconciler with specific distribution
			reconciler := &AgentReconciler{
				Client:       fakeClient,
				Scheme:       s,
				Distribution: tt.distribution,
			}

			// Create minimal valid Agent spec
			agent := &agentv1alpha1.Agent{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-agent",
					Namespace: "default",
				},
				Spec: agentv1alpha1.AgentSpec{
					PodTemplateSpec: &corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name:  "agent",
									Image: "test-image:latest",
								},
							},
						},
					},
					ImageSource: agentv1alpha1.ImageSource{
						Image: ptr.To("test-image:latest"),
					},
				},
			}

			// Create deployment
			deployment, err := reconciler.createDeploymentForAgent(context.Background(), agent)
			if err != nil {
				t.Fatalf("Failed to create deployment: %v", err)
			}

			// Verify security context
			podSecCtx := deployment.Spec.Template.Spec.SecurityContext
			if podSecCtx == nil {
				t.Fatal("Expected SecurityContext to be set, got nil")
			}

			// Check RunAsUser
			if tt.expectRunAsUser {
				if podSecCtx.RunAsUser == nil {
					t.Errorf("Expected RunAsUser to be set for %s distribution", tt.distribution)
				} else if *podSecCtx.RunAsUser != *tt.expectedRunAsUser {
					t.Errorf("Expected RunAsUser=%d, got %d", *tt.expectedRunAsUser, *podSecCtx.RunAsUser)
				}
			} else {
				if podSecCtx.RunAsUser != nil {
					t.Errorf("Expected RunAsUser to be nil for %s distribution, got %d", tt.distribution, *podSecCtx.RunAsUser)
				}
			}

			// Check FSGroup
			if tt.expectFSGroup {
				if podSecCtx.FSGroup == nil {
					t.Errorf("Expected FSGroup to be set for %s distribution", tt.distribution)
				} else if *podSecCtx.FSGroup != *tt.expectedFSGroup {
					t.Errorf("Expected FSGroup=%d, got %d", *tt.expectedFSGroup, *podSecCtx.FSGroup)
				}
			} else {
				if podSecCtx.FSGroup != nil {
					t.Errorf("Expected FSGroup to be nil for %s distribution, got %d", tt.distribution, *podSecCtx.FSGroup)
				}
			}

			// Verify other security context fields are always set
			if podSecCtx.RunAsNonRoot == nil || !*podSecCtx.RunAsNonRoot {
				t.Error("Expected RunAsNonRoot to be true")
			}

			if podSecCtx.SeccompProfile == nil || podSecCtx.SeccompProfile.Type != corev1.SeccompProfileTypeRuntimeDefault {
				t.Error("Expected SeccompProfile to be RuntimeDefault")
			}

			// Verify container security context
			for _, container := range deployment.Spec.Template.Spec.Containers {
				if container.SecurityContext == nil {
					t.Errorf("Expected container %s to have SecurityContext", container.Name)
					continue
				}

				containerSecCtx := container.SecurityContext
				if containerSecCtx.AllowPrivilegeEscalation == nil || *containerSecCtx.AllowPrivilegeEscalation {
					t.Errorf("Expected AllowPrivilegeEscalation to be false for container %s", container.Name)
				}
				if containerSecCtx.Privileged == nil || *containerSecCtx.Privileged {
					t.Errorf("Expected Privileged to be false for container %s", container.Name)
				}
				if containerSecCtx.ReadOnlyRootFilesystem == nil || !*containerSecCtx.ReadOnlyRootFilesystem {
					t.Errorf("Expected ReadOnlyRootFilesystem to be true for container %s", container.Name)
				}
			}
		})
	}
}

func TestSecurityContextUserOverride(t *testing.T) {
	// Setup scheme
	s := runtime.NewScheme()
	_ = scheme.AddToScheme(s)
	_ = agentv1alpha1.AddToScheme(s)

	// Create fake client
	fakeClient := fake.NewClientBuilder().WithScheme(s).Build()

	reconciler := &AgentReconciler{
		Client:       fakeClient,
		Scheme:       s,
		Distribution: distribution.Kubernetes,
	}

	// Create Agent with user-specified security context
	customUID := int64(2000)
	customGID := int64(3000)
	agent := &agentv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-agent",
			Namespace: "default",
		},
		Spec: agentv1alpha1.AgentSpec{
			PodTemplateSpec: &corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					SecurityContext: &corev1.PodSecurityContext{
						RunAsUser: &customUID,
						FSGroup:   &customGID,
					},
					Containers: []corev1.Container{
						{
							Name:  "agent",
							Image: "test-image:latest",
						},
					},
				},
			},
			ImageSource: agentv1alpha1.ImageSource{
				Image: ptr.To("test-image:latest"),
			},
		},
	}

	// Create deployment
	deployment, err := reconciler.createDeploymentForAgent(context.Background(), agent)
	if err != nil {
		t.Fatalf("Failed to create deployment: %v", err)
	}

	// Verify user-specified values are preserved
	podSecCtx := deployment.Spec.Template.Spec.SecurityContext
	if podSecCtx == nil {
		t.Fatal("Expected SecurityContext to be set")
	}

	if podSecCtx.RunAsUser == nil || *podSecCtx.RunAsUser != customUID {
		t.Errorf("Expected user-specified RunAsUser=%d to be preserved, got %v", customUID, podSecCtx.RunAsUser)
	}

	if podSecCtx.FSGroup == nil || *podSecCtx.FSGroup != customGID {
		t.Errorf("Expected user-specified FSGroup=%d to be preserved, got %v", customGID, podSecCtx.FSGroup)
	}
}
