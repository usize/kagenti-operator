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
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	aigatewayv1alpha1 "github.com/kagenti/operator/api/aigateway/v1alpha1"
	"github.com/kagenti/operator/internal/aigateway"
	"github.com/kagenti/operator/internal/aigateway/envoy"
	"github.com/kagenti/operator/internal/aigateway/spiffe"
)

const (
	// bundleRefreshInterval is how often the controller requeues to pick up
	// trust bundle rotations.
	bundleRefreshInterval = 5 * time.Minute
)

// AIAccessPolicyReconciler reconciles AIAccessPolicy resources.
type AIAccessPolicyReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
}

func (r *AIAccessPolicyReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.V(1).Info("Reconciling AIAccessPolicy")

	policy := &aigatewayv1alpha1.AIAccessPolicy{}
	if err := r.Get(ctx, req.NamespacedName, policy); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !policy.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}

	// Read the SPIFFE trust bundle ConfigMap
	bundleCM := &corev1.ConfigMap{}
	cmRef := policy.Spec.MTLS.TrustBundleConfigMap
	cmNamespace := cmRef.Namespace
	if cmNamespace == "" {
		cmNamespace = policy.Namespace
	}
	cmKey := cmRef.Key
	if cmKey == "" {
		cmKey = "bundle.spiffe"
	}

	if err := r.Get(ctx, types.NamespacedName{
		Name:      cmRef.Name,
		Namespace: cmNamespace,
	}, bundleCM); err != nil {
		logger.Error(err, "Failed to get trust bundle ConfigMap",
			"name", cmRef.Name, "namespace", cmNamespace)
		r.setCondition(ctx, policy, "Ready", metav1.ConditionFalse, "BundleNotFound",
			fmt.Sprintf("Trust bundle ConfigMap %s/%s not found: %v", cmNamespace, cmRef.Name, err))
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	raw, ok := bundleCM.Data[cmKey]
	if !ok || raw == "" {
		r.setCondition(ctx, policy, "Ready", metav1.ConditionFalse, "BundleEmpty",
			fmt.Sprintf("Trust bundle ConfigMap key %q not found or empty", cmKey))
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	// Convert bundle to PEM
	caPEM, err := spiffe.ConvertBundleData(raw)
	if err != nil {
		logger.Error(err, "Failed to convert trust bundle to PEM")
		r.setCondition(ctx, policy, "Ready", metav1.ConditionFalse, "BundleParseError", err.Error())
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	// Build AccessIntent
	intent := &aigateway.AccessIntent{
		PolicyName:      policy.Name,
		PolicyNamespace: policy.Namespace,
		GatewayName:     policy.Spec.TargetRef.Name,
		TrustDomain:     policy.Spec.MTLS.TrustDomain,
		CACertPEM:       caPEM,
	}
	if policy.Spec.MTLS.ServerCertRef != nil {
		intent.ServerCertRef = policy.Spec.MTLS.ServerCertRef.Name
	}

	// Render resources
	objects, err := envoy.RenderAccess(intent)
	if err != nil {
		logger.Error(err, "Failed to render access resources")
		r.setCondition(ctx, policy, "Ready", metav1.ConditionFalse, "RenderFailed", err.Error())
		return ctrl.Result{}, nil
	}

	// Apply each generated resource with owner reference
	for _, obj := range objects {
		if err := controllerutil.SetOwnerReference(policy, obj, r.Scheme); err != nil {
			logger.Error(err, "Failed to set owner reference")
			return ctrl.Result{}, err
		}

		existing := obj.DeepCopyObject().(client.Object)
		_, err := controllerutil.CreateOrUpdate(ctx, r.Client, existing, func() error {
			return controllerutil.SetOwnerReference(policy, existing, r.Scheme)
		})
		if err != nil {
			logger.Error(err, "Failed to apply resource",
				"kind", obj.GetObjectKind().GroupVersionKind().Kind,
				"name", obj.GetName())
			r.setCondition(ctx, policy, "Ready", metav1.ConditionFalse, "ApplyFailed", err.Error())
			return ctrl.Result{}, err
		}
	}

	logger.Info("AIAccessPolicy reconciled",
		"gateway", policy.Spec.TargetRef.Name,
		"trustDomain", policy.Spec.MTLS.TrustDomain,
		"resources", len(objects))

	r.setCondition(ctx, policy, "Ready", metav1.ConditionTrue, "Reconciled",
		fmt.Sprintf("Generated %d resources for mTLS access control", len(objects)))

	if r.Recorder != nil {
		r.Recorder.Eventf(policy, "Normal", "Reconciled",
			"Generated %d mTLS resources for gateway %s", len(objects), policy.Spec.TargetRef.Name)
	}

	// Requeue to pick up trust bundle rotations
	return ctrl.Result{RequeueAfter: bundleRefreshInterval}, nil
}

func (r *AIAccessPolicyReconciler) setCondition(ctx context.Context, policy *aigatewayv1alpha1.AIAccessPolicy,
	condType string, status metav1.ConditionStatus, reason, message string) {
	meta.SetStatusCondition(&policy.Status.Conditions, metav1.Condition{
		Type:               condType,
		Status:             status,
		ObservedGeneration: policy.Generation,
		Reason:             reason,
		Message:            message,
	})
	if err := r.Status().Update(ctx, policy); err != nil {
		log.FromContext(ctx).Error(err, "Failed to update status")
	}
}

func (r *AIAccessPolicyReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&aigatewayv1alpha1.AIAccessPolicy{}).
		Owns(&corev1.Secret{}).
		Named("aiaccesspolicy").
		Complete(r)
}
