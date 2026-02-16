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
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	netv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	agentv1alpha1 "github.com/kagenti/operator/api/v1alpha1"
)

const (
	// NetworkPolicyFinalizer is the finalizer for cleaning up network policies
	NetworkPolicyFinalizer = "agentcard.kagenti.dev/network-policy"

	// LabelSignatureVerified indicates if an agent's signature was verified
	LabelSignatureVerified = "agent.kagenti.dev/signature-verified"
)

var (
	networkPolicyLogger = ctrl.Log.WithName("controller").WithName("AgentCardNetworkPolicy")
)

// AgentCardNetworkPolicyReconciler reconciles AgentCard objects and manages NetworkPolicies
// based on signature verification status. It works with the new workload-based architecture
// (Deployments, StatefulSets) and also supports legacy Agent CRDs.
type AgentCardNetworkPolicyReconciler struct {
	client.Client
	Scheme                 *runtime.Scheme
	EnforceNetworkPolicies bool
	EnableLegacyAgentCRD   bool
}

// +kubebuilder:rbac:groups=networking.k8s.io,resources=networkpolicies,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;watch;update;patch

func (r *AgentCardNetworkPolicyReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	networkPolicyLogger.Info("Reconciling AgentCard NetworkPolicy", "namespacedName", req.NamespacedName)

	// Skip if network policy enforcement is disabled
	if !r.EnforceNetworkPolicies {
		return ctrl.Result{}, nil
	}

	agentCard := &agentv1alpha1.AgentCard{}
	err := r.Get(ctx, req.NamespacedName, agentCard)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// Handle deletion
	if !agentCard.ObjectMeta.DeletionTimestamp.IsZero() {
		return r.handleDeletion(ctx, agentCard)
	}

	// Add finalizer
	if !controllerutil.ContainsFinalizer(agentCard, NetworkPolicyFinalizer) {
		controllerutil.AddFinalizer(agentCard, NetworkPolicyFinalizer)
		if err := r.Update(ctx, agentCard); err != nil {
			networkPolicyLogger.Error(err, "Unable to add finalizer to AgentCard")
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	// Resolve the workload name and pod selector labels for the NetworkPolicy
	workloadName, podSelectorLabels, err := r.resolveWorkload(ctx, agentCard)
	if err != nil {
		networkPolicyLogger.Info("No workload resolved for AgentCard", "agentCard", agentCard.Name, "error", err)
		return ctrl.Result{}, nil
	}

	// Manage NetworkPolicy based on verification status
	if err := r.manageNetworkPolicy(ctx, agentCard, workloadName, podSelectorLabels); err != nil {
		networkPolicyLogger.Error(err, "Failed to manage NetworkPolicy")
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// resolveWorkload resolves the workload name and pod selector labels from the AgentCard.
// It supports targetRef (preferred), status.targetRef (resolved by main controller), and selector (legacy).
func (r *AgentCardNetworkPolicyReconciler) resolveWorkload(ctx context.Context, agentCard *agentv1alpha1.AgentCard) (string, map[string]string, error) {
	// Prefer spec.targetRef
	if agentCard.Spec.TargetRef != nil {
		ref := agentCard.Spec.TargetRef
		podLabels, err := r.getPodTemplateLabels(ctx, agentCard.Namespace, ref)
		if err != nil {
			return "", nil, err
		}
		return ref.Name, podLabels, nil
	}

	// Try status.targetRef (populated by AgentCardReconciler after resolving selector)
	if agentCard.Status.TargetRef != nil {
		ref := agentCard.Status.TargetRef
		podLabels, err := r.getPodTemplateLabels(ctx, agentCard.Namespace, ref)
		if err == nil {
			return ref.Name, podLabels, nil
		}
		// Fall through to selector if status targetRef lookup fails
	}

	// Fall back to selector (legacy)
	if agentCard.Spec.Selector != nil && len(agentCard.Spec.Selector.MatchLabels) > 0 {
		return agentCard.Name, agentCard.Spec.Selector.MatchLabels, nil
	}

	return "", nil, fmt.Errorf("neither targetRef nor selector specified")
}

// getPodTemplateLabels extracts the pod template labels from a workload using targetRef
func (r *AgentCardNetworkPolicyReconciler) getPodTemplateLabels(ctx context.Context, namespace string, ref *agentv1alpha1.TargetRef) (map[string]string, error) {
	key := types.NamespacedName{Name: ref.Name, Namespace: namespace}

	switch ref.Kind {
	case "Deployment":
		deployment := &appsv1.Deployment{}
		if err := r.Get(ctx, key, deployment); err != nil {
			return nil, err
		}
		return deployment.Spec.Template.Labels, nil

	case "StatefulSet":
		statefulset := &appsv1.StatefulSet{}
		if err := r.Get(ctx, key, statefulset); err != nil {
			return nil, err
		}
		return statefulset.Spec.Template.Labels, nil

	case "Agent":
		agent := &agentv1alpha1.Agent{}
		if err := r.Get(ctx, key, agent); err != nil {
			return nil, err
		}
		if agent.Spec.PodTemplateSpec != nil {
			return agent.Spec.PodTemplateSpec.Labels, nil
		}
		// Fallback: use agent labels
		return agent.Labels, nil

	default:
		// For unknown workload types, use the agent card name as a selector
		return map[string]string{
			LabelAgentType: LabelValueAgent,
			"app":          ref.Name,
		}, nil
	}
}

// manageNetworkPolicy creates or updates NetworkPolicy based on verification status
func (r *AgentCardNetworkPolicyReconciler) manageNetworkPolicy(ctx context.Context, agentCard *agentv1alpha1.AgentCard, workloadName string, podSelectorLabels map[string]string) error {
	policyName := fmt.Sprintf("%s-signature-policy", workloadName)

	// Determine if signature is verified
	isVerified := agentCard.Status.ValidSignature != nil && *agentCard.Status.ValidSignature

	if isVerified {
		return r.createPermissivePolicy(ctx, policyName, agentCard, podSelectorLabels)
	}
	return r.createRestrictivePolicy(ctx, policyName, agentCard, podSelectorLabels)
}

// createPermissivePolicy creates a NetworkPolicy that allows verified agents to communicate
func (r *AgentCardNetworkPolicyReconciler) createPermissivePolicy(ctx context.Context, policyName string, agentCard *agentv1alpha1.AgentCard, podSelectorLabels map[string]string) error {
	policy := &netv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      policyName,
			Namespace: agentCard.Namespace,
			Labels: map[string]string{
				"managed-by":              "kagenti-operator",
				"kagenti.dev/agentcard":   agentCard.Name,
				"kagenti.dev/policy-type": "signature-verification",
			},
		},
		Spec: netv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{
				MatchLabels: podSelectorLabels,
			},
			PolicyTypes: []netv1.PolicyType{
				netv1.PolicyTypeIngress,
				netv1.PolicyTypeEgress,
			},
			Ingress: []netv1.NetworkPolicyIngressRule{
				{
					From: []netv1.NetworkPolicyPeer{
						{
							// Allow traffic from other verified agents
							PodSelector: &metav1.LabelSelector{
								MatchLabels: map[string]string{
									LabelSignatureVerified: "true",
								},
							},
						},
						{
							// Allow traffic from operator/control plane
							NamespaceSelector: &metav1.LabelSelector{
								MatchLabels: map[string]string{
									"control-plane": "kagenti-operator",
								},
							},
						},
					},
				},
			},
			Egress: []netv1.NetworkPolicyEgressRule{
				{
					// Allow egress to other verified agents
					To: []netv1.NetworkPolicyPeer{
						{
							PodSelector: &metav1.LabelSelector{
								MatchLabels: map[string]string{
									LabelSignatureVerified: "true",
								},
							},
						},
					},
				},
				{
					// Allow DNS queries
					To: []netv1.NetworkPolicyPeer{
						{
							NamespaceSelector: &metav1.LabelSelector{
								MatchLabels: map[string]string{
									"kubernetes.io/metadata.name": "kube-system",
								},
							},
						},
					},
					Ports: []netv1.NetworkPolicyPort{
						{
							Protocol: func() *corev1.Protocol { p := corev1.ProtocolUDP; return &p }(),
							Port:     &intstr.IntOrString{Type: intstr.Int, IntVal: 53},
						},
						{
							Protocol: func() *corev1.Protocol { p := corev1.ProtocolTCP; return &p }(),
							Port:     &intstr.IntOrString{Type: intstr.Int, IntVal: 53},
						},
					},
				},
				{
					// Allow egress to API server
					To: []netv1.NetworkPolicyPeer{
						{
							NamespaceSelector: &metav1.LabelSelector{
								MatchLabels: map[string]string{
									"kubernetes.io/metadata.name": "default",
								},
							},
							PodSelector: &metav1.LabelSelector{
								MatchLabels: map[string]string{
									"component": "apiserver",
								},
							},
						},
					},
				},
			},
		},
	}

	// Set owner reference
	if err := controllerutil.SetControllerReference(agentCard, policy, r.Scheme); err != nil {
		return fmt.Errorf("failed to set controller reference: %w", err)
	}

	// Create or update the policy
	existingPolicy := &netv1.NetworkPolicy{}
	err := r.Get(ctx, types.NamespacedName{Name: policyName, Namespace: agentCard.Namespace}, existingPolicy)
	if err != nil {
		if apierrors.IsNotFound(err) {
			networkPolicyLogger.Info("Creating permissive NetworkPolicy for verified agent",
				"agentCard", agentCard.Name,
				"policy", policyName)
			return r.Create(ctx, policy)
		}
		return err
	}

	existingPolicy.Spec = policy.Spec
	networkPolicyLogger.Info("Updating NetworkPolicy to permissive for verified agent",
		"agentCard", agentCard.Name,
		"policy", policyName)
	return r.Update(ctx, existingPolicy)
}

// createRestrictivePolicy creates a NetworkPolicy that blocks unverified agents
func (r *AgentCardNetworkPolicyReconciler) createRestrictivePolicy(ctx context.Context, policyName string, agentCard *agentv1alpha1.AgentCard, podSelectorLabels map[string]string) error {
	policy := &netv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      policyName,
			Namespace: agentCard.Namespace,
			Labels: map[string]string{
				"managed-by":              "kagenti-operator",
				"kagenti.dev/agentcard":   agentCard.Name,
				"kagenti.dev/policy-type": "signature-verification",
			},
		},
		Spec: netv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{
				MatchLabels: podSelectorLabels,
			},
			PolicyTypes: []netv1.PolicyType{
				netv1.PolicyTypeIngress,
				netv1.PolicyTypeEgress,
			},
			Ingress: []netv1.NetworkPolicyIngressRule{
				{
					// Only allow traffic from operator for health checks
					From: []netv1.NetworkPolicyPeer{
						{
							NamespaceSelector: &metav1.LabelSelector{
								MatchLabels: map[string]string{
									"control-plane": "kagenti-operator",
								},
							},
						},
					},
				},
			},
			Egress: []netv1.NetworkPolicyEgressRule{
				{
					// Allow DNS queries only
					To: []netv1.NetworkPolicyPeer{
						{
							NamespaceSelector: &metav1.LabelSelector{
								MatchLabels: map[string]string{
									"kubernetes.io/metadata.name": "kube-system",
								},
							},
						},
					},
					Ports: []netv1.NetworkPolicyPort{
						{
							Protocol: func() *corev1.Protocol { p := corev1.ProtocolUDP; return &p }(),
							Port:     &intstr.IntOrString{Type: intstr.Int, IntVal: 53},
						},
						{
							Protocol: func() *corev1.Protocol { p := corev1.ProtocolTCP; return &p }(),
							Port:     &intstr.IntOrString{Type: intstr.Int, IntVal: 53},
						},
					},
				},
			},
		},
	}

	// Set owner reference
	if err := controllerutil.SetControllerReference(agentCard, policy, r.Scheme); err != nil {
		return fmt.Errorf("failed to set controller reference: %w", err)
	}

	// Create or update the policy
	existingPolicy := &netv1.NetworkPolicy{}
	err := r.Get(ctx, types.NamespacedName{Name: policyName, Namespace: agentCard.Namespace}, existingPolicy)
	if err != nil {
		if apierrors.IsNotFound(err) {
			networkPolicyLogger.Info("Creating restrictive NetworkPolicy for unverified agent",
				"agentCard", agentCard.Name,
				"policy", policyName,
				"reason", "signature not verified")
			return r.Create(ctx, policy)
		}
		return err
	}

	existingPolicy.Spec = policy.Spec
	networkPolicyLogger.Info("Updating NetworkPolicy to restrictive for unverified agent",
		"agentCard", agentCard.Name,
		"policy", policyName)
	return r.Update(ctx, existingPolicy)
}

// handleDeletion handles cleanup when an AgentCard is deleted
func (r *AgentCardNetworkPolicyReconciler) handleDeletion(ctx context.Context, agentCard *agentv1alpha1.AgentCard) (ctrl.Result, error) {
	if controllerutil.ContainsFinalizer(agentCard, NetworkPolicyFinalizer) {
		networkPolicyLogger.Info("Cleaning up NetworkPolicy for AgentCard", "name", agentCard.Name)

		// Determine the policy name from the resolved targetRef or card name
		workloadName := agentCard.Name
		if agentCard.Status.TargetRef != nil {
			workloadName = agentCard.Status.TargetRef.Name
		}
		policyName := fmt.Sprintf("%s-signature-policy", workloadName)

		// Delete the NetworkPolicy
		policy := &netv1.NetworkPolicy{}
		err := r.Get(ctx, types.NamespacedName{Name: policyName, Namespace: agentCard.Namespace}, policy)
		if err == nil {
			if err := r.Delete(ctx, policy); err != nil {
				networkPolicyLogger.Error(err, "Failed to delete NetworkPolicy")
				return ctrl.Result{}, err
			}
			networkPolicyLogger.Info("Deleted NetworkPolicy", "policy", policyName)
		}

		// Remove finalizer
		if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			latest := &agentv1alpha1.AgentCard{}
			if err := r.Get(ctx, types.NamespacedName{
				Name:      agentCard.Name,
				Namespace: agentCard.Namespace,
			}, latest); err != nil {
				return err
			}

			controllerutil.RemoveFinalizer(latest, NetworkPolicyFinalizer)
			return r.Update(ctx, latest)
		}); err != nil {
			networkPolicyLogger.Error(err, "Failed to remove finalizer from AgentCard")
			return ctrl.Result{}, err
		}

		networkPolicyLogger.Info("Removed finalizer from AgentCard")
	}

	return ctrl.Result{}, nil
}

// mapWorkloadToAgentCard maps Deployment/StatefulSet events to AgentCard reconcile requests
func (r *AgentCardNetworkPolicyReconciler) mapWorkloadToAgentCard(apiVersion, kind string) handler.MapFunc {
	return func(ctx context.Context, obj client.Object) []reconcile.Request {
		labels := obj.GetLabels()
		if !isAgentWorkload(labels) {
			return nil
		}

		agentCardList := &agentv1alpha1.AgentCardList{}
		if err := r.List(ctx, agentCardList, client.InNamespace(obj.GetNamespace())); err != nil {
			networkPolicyLogger.Error(err, "Failed to list AgentCards for mapping")
			return nil
		}

		var requests []reconcile.Request
		for _, agentCard := range agentCardList.Items {
			if agentCard.Spec.TargetRef != nil &&
				agentCard.Spec.TargetRef.Name == obj.GetName() &&
				agentCard.Spec.TargetRef.Kind == kind &&
				agentCard.Spec.TargetRef.APIVersion == apiVersion {
				requests = append(requests, reconcile.Request{
					NamespacedName: types.NamespacedName{
						Name:      agentCard.Name,
						Namespace: agentCard.Namespace,
					},
				})
			}
		}

		return requests
	}
}

// mapAgentToAgentCard maps Agent events to AgentCard reconcile requests
func (r *AgentCardNetworkPolicyReconciler) mapAgentToAgentCard(ctx context.Context, obj client.Object) []reconcile.Request {
	agent, ok := obj.(*agentv1alpha1.Agent)
	if !ok {
		return nil
	}

	if agent.Labels == nil || agent.Labels[LabelAgentType] != LabelValueAgent {
		return nil
	}

	agentCardList := &agentv1alpha1.AgentCardList{}
	if err := r.List(ctx, agentCardList, client.InNamespace(agent.Namespace)); err != nil {
		networkPolicyLogger.Error(err, "Failed to list AgentCards for mapping")
		return nil
	}

	var requests []reconcile.Request
	for _, agentCard := range agentCardList.Items {
		// Check targetRef
		if agentCard.Spec.TargetRef != nil &&
			agentCard.Spec.TargetRef.Name == agent.Name &&
			agentCard.Spec.TargetRef.Kind == "Agent" {
			requests = append(requests, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      agentCard.Name,
					Namespace: agentCard.Namespace,
				},
			})
			continue
		}
		// Check selector
		if agentCard.Spec.Selector != nil && agent.Labels != nil {
			match := true
			for key, value := range agentCard.Spec.Selector.MatchLabels {
				if agent.Labels[key] != value {
					match = false
					break
				}
			}
			if match {
				requests = append(requests, reconcile.Request{
					NamespacedName: types.NamespacedName{
						Name:      agentCard.Name,
						Namespace: agentCard.Namespace,
					},
				})
			}
		}
	}

	return requests
}

// SetupWithManager sets up the controller with the Manager.
func (r *AgentCardNetworkPolicyReconciler) SetupWithManager(mgr ctrl.Manager) error {
	controllerBuilder := ctrl.NewControllerManagedBy(mgr).
		For(&agentv1alpha1.AgentCard{}).
		Owns(&netv1.NetworkPolicy{}).
		// Watch Deployments with agent labels
		Watches(
			&appsv1.Deployment{},
			handler.EnqueueRequestsFromMapFunc(r.mapWorkloadToAgentCard("apps/v1", "Deployment")),
			builder.WithPredicates(agentLabelPredicate()),
		).
		// Watch StatefulSets with agent labels
		Watches(
			&appsv1.StatefulSet{},
			handler.EnqueueRequestsFromMapFunc(r.mapWorkloadToAgentCard("apps/v1", "StatefulSet")),
			builder.WithPredicates(agentLabelPredicate()),
		)

	// Optionally watch legacy Agent CRDs if enabled
	if r.EnableLegacyAgentCRD {
		controllerBuilder = controllerBuilder.Watches(
			&agentv1alpha1.Agent{},
			handler.EnqueueRequestsFromMapFunc(r.mapAgentToAgentCard),
			builder.WithPredicates(agentLabelPredicate()),
		)
	}

	return controllerBuilder.
		Named("AgentCardNetworkPolicy").
		Complete(r)
}
