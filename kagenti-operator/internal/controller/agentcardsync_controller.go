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

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	agentv1alpha1 "github.com/kagenti/operator/api/v1alpha1"
)

var (
	syncLogger = ctrl.Log.WithName("controller").WithName("AgentCardSync")
)

// AgentCardSyncReconciler automatically creates AgentCard resources for Agents
type AgentCardSyncReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=agent.kagenti.dev,resources=agents,verbs=get;list;watch
// +kubebuilder:rbac:groups=agent.kagenti.dev,resources=agentcards,verbs=get;list;watch;create;update;patch;delete

func (r *AgentCardSyncReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	syncLogger.Info("Reconciling Agent for auto-sync", "namespacedName", req.NamespacedName)

	agent := &agentv1alpha1.Agent{}
	err := r.Get(ctx, req.NamespacedName, agent)
	if err != nil {
		if errors.IsNotFound(err) {
			// Agent deleted, check for orphaned AgentCards
			return r.cleanupOrphanedCards(ctx, req.NamespacedName)
		}
		return ctrl.Result{}, err
	}

	// Check if Agent has the required labels
	if !r.shouldSyncAgent(agent) {
		syncLogger.Info("Agent does not have agent card labels, skipping", "agent", agent.Name)
		return ctrl.Result{}, nil
	}

	// Check if AgentCard already exists
	agentCardName := r.getAgentCardName(agent)
	agentCard := &agentv1alpha1.AgentCard{}
	err = r.Get(ctx, types.NamespacedName{
		Name:      agentCardName,
		Namespace: agent.Namespace,
	}, agentCard)

	if err != nil && errors.IsNotFound(err) {
		// Create new AgentCard
		return r.createAgentCard(ctx, agent)
	} else if err != nil {
		syncLogger.Error(err, "Failed to get AgentCard")
		return ctrl.Result{}, err
	}

	// AgentCard exists, ensure owner reference is set
	if !r.hasOwnerReference(agentCard, agent) {
		syncLogger.Info("Adding owner reference to existing AgentCard", "agentCard", agentCard.Name)
		if err := controllerutil.SetControllerReference(agent, agentCard, r.Scheme); err != nil {
			syncLogger.Error(err, "Failed to set owner reference")
			return ctrl.Result{}, err
		}
		if err := r.Update(ctx, agentCard); err != nil {
			syncLogger.Error(err, "Failed to update AgentCard with owner reference")
			return ctrl.Result{}, err
		}
	}

	return ctrl.Result{}, nil
}

// shouldSyncAgent checks if an Agent should have an AgentCard created
func (r *AgentCardSyncReconciler) shouldSyncAgent(agent *agentv1alpha1.Agent) bool {
	if agent.Labels == nil {
		return false
	}

	// Must have kagenti.io/type=agent label
	if agent.Labels[LabelAgentType] != LabelValueAgent {
		return false
	}

	// Must have a protocol label to know how to fetch the card
	if agent.Labels[LabelAgentProtocol] == "" {
		syncLogger.Info("Agent has type=agent but no protocol label", "agent", agent.Name)
		return false
	}

	return true
}

// getAgentCardName generates the AgentCard name for an Agent
func (r *AgentCardSyncReconciler) getAgentCardName(agent *agentv1alpha1.Agent) string {
	return agent.Name + "-card"
}

// createAgentCard creates a new AgentCard for an Agent
func (r *AgentCardSyncReconciler) createAgentCard(ctx context.Context, agent *agentv1alpha1.Agent) (ctrl.Result, error) {
	syncLogger.Info("Creating AgentCard for Agent", "agent", agent.Name)

	agentCard := &agentv1alpha1.AgentCard{
		ObjectMeta: metav1.ObjectMeta{
			Name:      r.getAgentCardName(agent),
			Namespace: agent.Namespace,
			Labels: map[string]string{
				"app.kubernetes.io/name":       agent.Name,
				"app.kubernetes.io/managed-by": "kagenti-operator",
			},
		},
		Spec: agentv1alpha1.AgentCardSpec{
			SyncPeriod: "30s",
			Selector: agentv1alpha1.AgentSelector{
				MatchLabels: map[string]string{
					"app.kubernetes.io/name": agent.Name,
					LabelAgentType:           LabelValueAgent,
				},
			},
		},
	}

	// Set owner reference for garbage collection
	if err := controllerutil.SetControllerReference(agent, agentCard, r.Scheme); err != nil {
		syncLogger.Error(err, "Failed to set controller reference for AgentCard")
		return ctrl.Result{}, err
	}

	if err := r.Create(ctx, agentCard); err != nil {
		if errors.IsAlreadyExists(err) {
			syncLogger.Info("AgentCard already exists", "agentCard", agentCard.Name)
			return ctrl.Result{}, nil
		}
		syncLogger.Error(err, "Failed to create AgentCard")
		return ctrl.Result{}, err
	}

	syncLogger.Info("Successfully created AgentCard", "agentCard", agentCard.Name)
	return ctrl.Result{}, nil
}

// hasOwnerReference checks if an AgentCard has the correct owner reference
func (r *AgentCardSyncReconciler) hasOwnerReference(agentCard *agentv1alpha1.AgentCard, agent *agentv1alpha1.Agent) bool {
	for _, ownerRef := range agentCard.OwnerReferences {
		if ownerRef.UID == agent.UID {
			return true
		}
	}
	return false
}

// cleanupOrphanedCards removes AgentCard resources for deleted Agents
func (r *AgentCardSyncReconciler) cleanupOrphanedCards(ctx context.Context, agentName types.NamespacedName) (ctrl.Result, error) {
	// Check if there's an AgentCard that should be cleaned up
	agentCardName := fmt.Sprintf("%s-card", agentName.Name)
	agentCard := &agentv1alpha1.AgentCard{}

	err := r.Get(ctx, types.NamespacedName{
		Name:      agentCardName,
		Namespace: agentName.Namespace,
	}, agentCard)

	if err != nil {
		// AgentCard doesn't exist or was already deleted, nothing to do
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Check if this AgentCard is orphaned (no valid owner reference)
	hasValidOwner := false
	for _, ownerRef := range agentCard.OwnerReferences {
		if ownerRef.Kind == "Agent" {
			// Try to get the owner Agent
			agent := &agentv1alpha1.Agent{}
			err := r.Get(ctx, types.NamespacedName{
				Name:      ownerRef.Name,
				Namespace: agentCard.Namespace,
			}, agent)
			if err == nil {
				hasValidOwner = true
				break
			}
		}
	}

	if !hasValidOwner {
		syncLogger.Info("Deleting orphaned AgentCard", "agentCard", agentCard.Name)
		if err := r.Delete(ctx, agentCard); err != nil {
			syncLogger.Error(err, "Failed to delete orphaned AgentCard")
			return ctrl.Result{}, err
		}
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *AgentCardSyncReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&agentv1alpha1.Agent{}).
		Named("AgentCardSync").
		Complete(r)
}
