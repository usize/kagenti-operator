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
	"net/url"
	"strconv"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	aigatewayv1alpha1 "github.com/kagenti/operator/api/aigateway/v1alpha1"
	"github.com/kagenti/operator/internal/aigateway"
	"github.com/kagenti/operator/internal/aigateway/envoy"
)

const (
	aiRoutingPolicyFinalizer = "aigateway.kagenti.dev/routing-cleanup"
)

// AIRoutingPolicyReconciler reconciles AIRoutingPolicy resources.
type AIRoutingPolicyReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
}

func (r *AIRoutingPolicyReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.V(1).Info("Reconciling AIRoutingPolicy")

	policy := &aigatewayv1alpha1.AIRoutingPolicy{}
	if err := r.Get(ctx, req.NamespacedName, policy); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Handle deletion
	if !policy.DeletionTimestamp.IsZero() {
		if controllerutil.ContainsFinalizer(policy, aiRoutingPolicyFinalizer) {
			controllerutil.RemoveFinalizer(policy, aiRoutingPolicyFinalizer)
			if err := r.Update(ctx, policy); err != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{}, nil
	}

	// Add finalizer if needed
	if !controllerutil.ContainsFinalizer(policy, aiRoutingPolicyFinalizer) {
		controllerutil.AddFinalizer(policy, aiRoutingPolicyFinalizer)
		if err := r.Update(ctx, policy); err != nil {
			return ctrl.Result{}, err
		}
	}

	// Convert spec to RoutingIntent
	intent, err := r.buildRoutingIntent(policy)
	if err != nil {
		logger.Error(err, "Failed to build routing intent")
		r.setCondition(ctx, policy, "Ready", metav1.ConditionFalse, "InvalidSpec", err.Error())
		return ctrl.Result{}, nil
	}

	// Render Envoy resources
	objects := envoy.RenderRouting(intent)

	// Apply each generated resource with owner reference
	for _, obj := range objects {
		if err := r.applyResource(ctx, policy, obj); err != nil {
			logger.Error(err, "Failed to apply resource",
				"kind", obj.GetObjectKind().GroupVersionKind().Kind,
				"name", obj.GetName())
			r.setCondition(ctx, policy, "Ready", metav1.ConditionFalse, "ApplyFailed", err.Error())
			return ctrl.Result{}, err
		}
	}

	logger.Info("AIRoutingPolicy reconciled",
		"providers", len(policy.Spec.Providers),
		"models", len(policy.Spec.Models),
		"resources", len(objects))

	r.setCondition(ctx, policy, "Ready", metav1.ConditionTrue, "Reconciled",
		fmt.Sprintf("Generated %d resources for %d providers and %d models",
			len(objects), len(policy.Spec.Providers), len(policy.Spec.Models)))

	if r.Recorder != nil {
		r.Recorder.Eventf(policy, "Normal", "Reconciled",
			"Generated %d Envoy resources", len(objects))
	}

	return ctrl.Result{}, nil
}

func (r *AIRoutingPolicyReconciler) buildRoutingIntent(policy *aigatewayv1alpha1.AIRoutingPolicy) (*aigateway.RoutingIntent, error) {
	intent := &aigateway.RoutingIntent{
		PolicyName:      policy.Name,
		PolicyNamespace: policy.Namespace,
		GatewayName:     policy.Spec.TargetRef.Name,
	}

	for _, p := range policy.Spec.Providers {
		host, port, err := parseEndpoint(p.Endpoint)
		if err != nil {
			return nil, fmt.Errorf("provider %q: %w", p.Name, err)
		}
		pi := aigateway.ProviderIntent{
			Name:   p.Name,
			Host:   host,
			Port:   port,
			Schema: p.Schema,
		}
		if p.Credentials != nil {
			pi.Credentials = &aigateway.CredentialIntent{
				Type: p.Credentials.Type,
			}
			if p.Credentials.SecretRef != nil {
				pi.Credentials.SecretName = p.Credentials.SecretRef.Name
				pi.Credentials.SecretKey = p.Credentials.SecretRef.Key
			}
			pi.Credentials.Region = p.Credentials.Region
			pi.Credentials.TenantId = p.Credentials.TenantId
			pi.Credentials.ClientId = p.Credentials.ClientId
			if p.Credentials.ClientSecretRef != nil {
				pi.Credentials.ClientSecretName = p.Credentials.ClientSecretRef.Name
				pi.Credentials.ClientSecretKey = p.Credentials.ClientSecretRef.Key
			}
		}
		intent.Providers = append(intent.Providers, pi)
	}

	for _, m := range policy.Spec.Models {
		mi := aigateway.ModelIntent{
			Name: m.Name,
		}
		for _, b := range m.Backends {
			mi.Backends = append(mi.Backends, aigateway.ModelBackendIntent{
				ProviderName: b.Provider,
				ModelName:    b.Model,
				Priority:     b.Priority,
			})
		}
		if m.RateLimit != nil {
			mi.RateLimit = &aigateway.RateLimitIntent{
				RequestsPerMinute: m.RateLimit.RequestsPerMinute,
			}
		}
		if m.Failover != nil {
			mi.Failover = &aigateway.FailoverIntent{
				RetryOn:    m.Failover.RetryOn,
				MaxRetries: m.Failover.MaxRetries,
			}
		}
		intent.Models = append(intent.Models, mi)
	}

	return intent, nil
}

func (r *AIRoutingPolicyReconciler) applyResource(ctx context.Context, owner *aigatewayv1alpha1.AIRoutingPolicy, obj client.Object) error {
	if err := controllerutil.SetOwnerReference(owner, obj, r.Scheme); err != nil {
		return fmt.Errorf("setting owner reference: %w", err)
	}

	existing := obj.DeepCopyObject().(client.Object)
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, existing, func() error {
		// Copy spec from desired to existing by replacing the full object
		// This works because CreateOrUpdate handles the metadata merge
		return controllerutil.SetOwnerReference(owner, existing, r.Scheme)
	})
	return err
}

func (r *AIRoutingPolicyReconciler) setCondition(ctx context.Context, policy *aigatewayv1alpha1.AIRoutingPolicy,
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

// parseEndpoint extracts host and port from a URL string.
func parseEndpoint(endpoint string) (string, int32, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", 0, fmt.Errorf("invalid endpoint URL %q: %w", endpoint, err)
	}

	host := u.Hostname()
	if host == "" {
		return "", 0, fmt.Errorf("endpoint URL %q has no hostname", endpoint)
	}

	portStr := u.Port()
	if portStr != "" {
		port, err := strconv.ParseInt(portStr, 10, 32)
		if err != nil {
			return "", 0, fmt.Errorf("invalid port in endpoint URL %q: %w", endpoint, err)
		}
		return host, int32(port), nil
	}

	// Default ports by scheme
	switch u.Scheme {
	case "https":
		return host, 443, nil
	default:
		return host, 80, nil
	}
}

func (r *AIRoutingPolicyReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&aigatewayv1alpha1.AIRoutingPolicy{}).
		Named("airoutingpolicy").
		Complete(r)
}
