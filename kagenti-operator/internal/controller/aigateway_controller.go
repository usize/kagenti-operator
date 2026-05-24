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
	"net/url"
	"strconv"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/source"

	gatewayv1alpha1 "github.com/kagenti/operator/api/gateway/v1alpha1"
)

const (
	AIGatewayFinalizer = "gateway.kagenti.dev/finalizer"

	// Envoy AI Gateway API group and versions.
	envoyAIGatewayGroup   = "aigateway.envoyproxy.io"
	envoyAIGatewayVersion = "v1alpha1"

	// Gateway API group and version.
	gatewayAPIGroup   = "gateway.networking.k8s.io"
	gatewayAPIVersion = "v1"
)

var (
	aiGatewayLogger = ctrl.Log.WithName("controller").WithName("AIGateway")

	gvkGateway               = schema.GroupVersionKind{Group: gatewayAPIGroup, Version: gatewayAPIVersion, Kind: "Gateway"}
	gvkAIServiceBackend      = schema.GroupVersionKind{Group: envoyAIGatewayGroup, Version: envoyAIGatewayVersion, Kind: "AIServiceBackend"}
	gvkBackendSecurityPolicy = schema.GroupVersionKind{Group: envoyAIGatewayGroup, Version: envoyAIGatewayVersion, Kind: "BackendSecurityPolicy"}
	gvkAIGatewayRoute        = schema.GroupVersionKind{Group: envoyAIGatewayGroup, Version: envoyAIGatewayVersion, Kind: "AIGatewayRoute"}
)

// AIGatewayReconciler reconciles an AIGateway object.
type AIGatewayReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
}

// +kubebuilder:rbac:groups=gateway.kagenti.dev,resources=aigateways,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=gateway.kagenti.dev,resources=aigateways/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=gateway.kagenti.dev,resources=aigateways/finalizers,verbs=update
// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=gateways,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=aigateway.envoyproxy.io,resources=aiservicebackends,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=aigateway.envoyproxy.io,resources=backendsecuritypolicies,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=aigateway.envoyproxy.io,resources=aigatewayroutes,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=secrets,verbs=get;list;watch

func (r *AIGatewayReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	aiGatewayLogger.V(1).Info("Reconciling AIGateway", "namespacedName", req.NamespacedName)

	aigw := &gatewayv1alpha1.AIGateway{}
	if err := r.Get(ctx, req.NamespacedName, aigw); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Handle deletion.
	if !aigw.DeletionTimestamp.IsZero() {
		return r.handleDeletion(ctx, aigw)
	}

	// Add finalizer if missing.
	if !controllerutil.ContainsFinalizer(aigw, AIGatewayFinalizer) {
		controllerutil.AddFinalizer(aigw, AIGatewayFinalizer)
		if err := r.Update(ctx, aigw); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	// Build and reconcile all downstream resources.
	if err := r.reconcileGateway(ctx, aigw); err != nil {
		r.setCondition(ctx, aigw, "Ready", metav1.ConditionFalse, "GatewayFailed", err.Error())
		return ctrl.Result{RequeueAfter: 30 * time.Second}, err
	}

	if err := r.reconcileBackends(ctx, aigw); err != nil {
		r.setCondition(ctx, aigw, "Ready", metav1.ConditionFalse, "BackendsFailed", err.Error())
		return ctrl.Result{RequeueAfter: 30 * time.Second}, err
	}

	if err := r.reconcileRoute(ctx, aigw); err != nil {
		r.setCondition(ctx, aigw, "Ready", metav1.ConditionFalse, "RouteFailed", err.Error())
		return ctrl.Result{RequeueAfter: 30 * time.Second}, err
	}

	// Update status.
	if err := r.updateStatus(ctx, aigw); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{RequeueAfter: 60 * time.Second}, nil
}

// reconcileGateway creates or updates the Gateway API Gateway resource.
func (r *AIGatewayReconciler) reconcileGateway(ctx context.Context, aigw *gatewayv1alpha1.AIGateway) error {
	gw := &unstructured.Unstructured{}
	gw.SetGroupVersionKind(gvkGateway)
	gw.SetName(aigw.Name)
	gw.SetNamespace(aigw.Namespace)

	listeners := make([]interface{}, 0, len(aigw.Spec.Listeners))
	for _, l := range aigw.Spec.Listeners {
		protocol := l.Protocol
		if protocol == "" {
			protocol = "HTTP"
		}
		listeners = append(listeners, map[string]interface{}{
			"name":     l.Name,
			"port":     int64(l.Port),
			"protocol": protocol,
		})
	}

	desired := map[string]interface{}{
		"spec": map[string]interface{}{
			"gatewayClassName": aigw.Spec.GatewayClassName,
			"listeners":        listeners,
		},
	}

	return r.createOrUpdate(ctx, aigw, gw, desired)
}

// reconcileBackends creates AIServiceBackend and BackendSecurityPolicy for each provider.
func (r *AIGatewayReconciler) reconcileBackends(ctx context.Context, aigw *gatewayv1alpha1.AIGateway) error {
	for _, provider := range aigw.Spec.Providers {
		if err := r.reconcileAIServiceBackend(ctx, aigw, provider); err != nil {
			return fmt.Errorf("provider %s backend: %w", provider.Name, err)
		}
		if provider.CredentialRef != nil {
			if err := r.reconcileBackendSecurityPolicy(ctx, aigw, provider); err != nil {
				return fmt.Errorf("provider %s security policy: %w", provider.Name, err)
			}
		}
	}

	// Clean up backends and policies for removed providers.
	return r.cleanupRemovedProviders(ctx, aigw)
}

// reconcileAIServiceBackend creates or updates an AIServiceBackend for a provider.
func (r *AIGatewayReconciler) reconcileAIServiceBackend(ctx context.Context, aigw *gatewayv1alpha1.AIGateway, provider gatewayv1alpha1.AIGatewayProvider) error {
	backend := &unstructured.Unstructured{}
	backend.SetGroupVersionKind(gvkAIServiceBackend)
	backend.SetName(backendName(aigw.Name, provider.Name))
	backend.SetNamespace(aigw.Namespace)

	parsedURL, err := url.Parse(provider.Endpoint)
	if err != nil {
		return fmt.Errorf("invalid endpoint URL %q: %w", provider.Endpoint, err)
	}

	host := parsedURL.Hostname()
	portStr := parsedURL.Port()
	port := int64(443)
	if portStr != "" {
		p, err := strconv.ParseInt(portStr, 10, 32)
		if err != nil {
			return fmt.Errorf("invalid port in endpoint %q: %w", provider.Endpoint, err)
		}
		port = p
	} else if parsedURL.Scheme == "http" {
		port = 80
	}

	backendRef := map[string]interface{}{
		"name": host,
		"port": port,
	}

	// Determine if this is a FQDN (external) or a Kubernetes service (in-cluster).
	if strings.Contains(host, ".svc") || !strings.Contains(host, ".") {
		backendRef["kind"] = "Service"
		backendRef["group"] = ""
	} else {
		backendRef["kind"] = "Backend"
		backendRef["group"] = "gateway.envoyproxy.io"
	}

	desired := map[string]interface{}{
		"spec": map[string]interface{}{
			"schema": map[string]interface{}{
				"name": provider.Schema,
			},
			"backendRef": backendRef,
		},
	}

	return r.createOrUpdate(ctx, aigw, backend, desired)
}

// reconcileBackendSecurityPolicy creates or updates a BackendSecurityPolicy for a provider with credentials.
func (r *AIGatewayReconciler) reconcileBackendSecurityPolicy(ctx context.Context, aigw *gatewayv1alpha1.AIGateway, provider gatewayv1alpha1.AIGatewayProvider) error {
	policy := &unstructured.Unstructured{}
	policy.SetGroupVersionKind(gvkBackendSecurityPolicy)
	policy.SetName(policyName(aigw.Name, provider.Name))
	policy.SetNamespace(aigw.Namespace)

	credentialKey := provider.CredentialKey
	if credentialKey == "" {
		credentialKey = "api-key"
	}

	desired := map[string]interface{}{
		"spec": map[string]interface{}{
			"targetRefs": []interface{}{
				map[string]interface{}{
					"group": envoyAIGatewayGroup,
					"kind":  "AIServiceBackend",
					"name":  backendName(aigw.Name, provider.Name),
				},
			},
			"apiKey": map[string]interface{}{
				"secretRef": map[string]interface{}{
					"name":      provider.CredentialRef.Name,
					"namespace": aigw.Namespace,
				},
			},
		},
	}

	return r.createOrUpdate(ctx, aigw, policy, desired)
}

// reconcileRoute creates or updates the AIGatewayRoute with rules mapping each model to its backend.
func (r *AIGatewayReconciler) reconcileRoute(ctx context.Context, aigw *gatewayv1alpha1.AIGateway) error {
	route := &unstructured.Unstructured{}
	route.SetGroupVersionKind(gvkAIGatewayRoute)
	route.SetName(aigw.Name)
	route.SetNamespace(aigw.Namespace)

	rules := make([]interface{}, 0)
	for _, provider := range aigw.Spec.Providers {
		for _, model := range provider.Models {
			rules = append(rules, map[string]interface{}{
				"matches": []interface{}{
					map[string]interface{}{
						"headers": []interface{}{
							map[string]interface{}{
								"type":  "Exact",
								"name":  "x-ai-eg-model",
								"value": model,
							},
						},
					},
				},
				"backendRefs": []interface{}{
					map[string]interface{}{
						"name":  backendName(aigw.Name, provider.Name),
						"kind":  "AIServiceBackend",
						"group": envoyAIGatewayGroup,
					},
				},
			})
		}
	}

	desired := map[string]interface{}{
		"spec": map[string]interface{}{
			"parentRefs": []interface{}{
				map[string]interface{}{
					"name":      aigw.Name,
					"namespace": aigw.Namespace,
					"group":     gatewayAPIGroup,
					"kind":      "Gateway",
				},
			},
			"rules": rules,
		},
	}

	return r.createOrUpdate(ctx, aigw, route, desired)
}

// createOrUpdate reconciles an unstructured resource with create-or-update semantics.
func (r *AIGatewayReconciler) createOrUpdate(ctx context.Context, aigw *gatewayv1alpha1.AIGateway, obj *unstructured.Unstructured, desired map[string]interface{}) error {
	name := obj.GetName()
	namespace := obj.GetNamespace()
	gvk := obj.GroupVersionKind()

	existing := &unstructured.Unstructured{}
	existing.SetGroupVersionKind(gvk)

	err := r.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, existing)
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return err
		}

		// Create.
		for k, v := range desired {
			obj.Object[k] = v
		}
		if err := controllerutil.SetOwnerReference(aigw, obj, r.Scheme); err != nil {
			return fmt.Errorf("setting owner reference: %w", err)
		}
		if err := r.Create(ctx, obj); err != nil {
			return fmt.Errorf("creating %s %s: %w", gvk.Kind, name, err)
		}
		aiGatewayLogger.Info("Created resource", "kind", gvk.Kind, "name", name)
		if r.Recorder != nil {
			r.Recorder.Eventf(aigw, "Normal", "Created", "Created %s %s", gvk.Kind, name)
		}
		return nil
	}

	// Update.
	for k, v := range desired {
		existing.Object[k] = v
	}
	if err := r.Update(ctx, existing); err != nil {
		return fmt.Errorf("updating %s %s: %w", gvk.Kind, name, err)
	}
	aiGatewayLogger.V(1).Info("Updated resource", "kind", gvk.Kind, "name", name)
	return nil
}

// cleanupRemovedProviders removes AIServiceBackend and BackendSecurityPolicy resources
// for providers that are no longer in the spec.
func (r *AIGatewayReconciler) cleanupRemovedProviders(ctx context.Context, aigw *gatewayv1alpha1.AIGateway) error {
	currentProviders := make(map[string]bool)
	for _, p := range aigw.Spec.Providers {
		currentProviders[p.Name] = true
	}

	// Clean up AIServiceBackends.
	backends := &unstructured.UnstructuredList{}
	backends.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   envoyAIGatewayGroup,
		Version: envoyAIGatewayVersion,
		Kind:    "AIServiceBackendList",
	})
	if err := r.List(ctx, backends, client.InNamespace(aigw.Namespace)); err != nil {
		if !apierrors.IsNotFound(err) && !meta.IsNoMatchError(err) {
			return err
		}
		return nil
	}

	prefix := aigw.Name + "-"
	for i := range backends.Items {
		b := &backends.Items[i]
		if !strings.HasPrefix(b.GetName(), prefix) {
			continue
		}
		if !isOwnedBy(b, aigw) {
			continue
		}
		providerName := strings.TrimPrefix(b.GetName(), prefix)
		if !currentProviders[providerName] {
			if err := r.Delete(ctx, b); err != nil && !apierrors.IsNotFound(err) {
				return err
			}
			aiGatewayLogger.Info("Deleted orphaned AIServiceBackend", "name", b.GetName())
		}
	}

	// Clean up BackendSecurityPolicies.
	policies := &unstructured.UnstructuredList{}
	policies.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   envoyAIGatewayGroup,
		Version: envoyAIGatewayVersion,
		Kind:    "BackendSecurityPolicyList",
	})
	if err := r.List(ctx, policies, client.InNamespace(aigw.Namespace)); err != nil {
		if !apierrors.IsNotFound(err) && !meta.IsNoMatchError(err) {
			return err
		}
		return nil
	}

	policyPrefix := aigw.Name + "-"
	for i := range policies.Items {
		p := &policies.Items[i]
		if !strings.HasPrefix(p.GetName(), policyPrefix) {
			continue
		}
		if !isOwnedBy(p, aigw) {
			continue
		}
		// Policy names end with "-credentials"
		trimmed := strings.TrimPrefix(p.GetName(), policyPrefix)
		providerName := strings.TrimSuffix(trimmed, "-credentials")
		if !currentProviders[providerName] {
			if err := r.Delete(ctx, p); err != nil && !apierrors.IsNotFound(err) {
				return err
			}
			aiGatewayLogger.Info("Deleted orphaned BackendSecurityPolicy", "name", p.GetName())
		}
	}

	return nil
}

// updateStatus updates the AIGateway status with endpoint, conditions, and counts.
func (r *AIGatewayReconciler) updateStatus(ctx context.Context, aigw *gatewayv1alpha1.AIGateway) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		latest := &gatewayv1alpha1.AIGateway{}
		if err := r.Get(ctx, types.NamespacedName{
			Name:      aigw.Name,
			Namespace: aigw.Namespace,
		}, latest); err != nil {
			return err
		}

		// Count models and providers.
		var modelCount int32
		for _, p := range latest.Spec.Providers {
			modelCount += int32(len(p.Models))
		}
		latest.Status.ConfiguredModels = modelCount
		latest.Status.ConfiguredProviders = int32(len(latest.Spec.Providers))
		latest.Status.ObservedGeneration = latest.Generation

		// Check Gateway status.
		gatewayReady := r.isGatewayReady(ctx, latest)

		if gatewayReady {
			latest.Status.Endpoint = r.computeEndpoint(latest)
			meta.SetStatusCondition(&latest.Status.Conditions, metav1.Condition{
				Type:               "GatewayReady",
				Status:             metav1.ConditionTrue,
				Reason:             "Programmed",
				Message:            "Gateway is programmed",
				ObservedGeneration: latest.Generation,
			})
		} else {
			meta.SetStatusCondition(&latest.Status.Conditions, metav1.Condition{
				Type:               "GatewayReady",
				Status:             metav1.ConditionFalse,
				Reason:             "NotProgrammed",
				Message:            "Waiting for Gateway to be programmed",
				ObservedGeneration: latest.Generation,
			})
		}

		meta.SetStatusCondition(&latest.Status.Conditions, metav1.Condition{
			Type:               "RoutesConfigured",
			Status:             metav1.ConditionTrue,
			Reason:             "Configured",
			Message:            fmt.Sprintf("%d model routes configured across %d providers", modelCount, len(latest.Spec.Providers)),
			ObservedGeneration: latest.Generation,
		})

		readyStatus := metav1.ConditionTrue
		readyReason := "Ready"
		readyMessage := fmt.Sprintf("AIGateway is ready with %d providers and %d models", len(latest.Spec.Providers), modelCount)
		if !gatewayReady {
			readyStatus = metav1.ConditionFalse
			readyReason = "GatewayNotReady"
			readyMessage = "Waiting for Gateway to be programmed"
		}
		meta.SetStatusCondition(&latest.Status.Conditions, metav1.Condition{
			Type:               "Ready",
			Status:             readyStatus,
			Reason:             readyReason,
			Message:            readyMessage,
			ObservedGeneration: latest.Generation,
		})

		return r.Status().Update(ctx, latest)
	})
}

// isGatewayReady checks the Gateway resource for the Programmed condition.
func (r *AIGatewayReconciler) isGatewayReady(ctx context.Context, aigw *gatewayv1alpha1.AIGateway) bool {
	gw := &unstructured.Unstructured{}
	gw.SetGroupVersionKind(gvkGateway)
	if err := r.Get(ctx, types.NamespacedName{Name: aigw.Name, Namespace: aigw.Namespace}, gw); err != nil {
		return false
	}

	conditions, found, err := unstructured.NestedSlice(gw.Object, "status", "conditions")
	if err != nil || !found {
		return false
	}

	for _, c := range conditions {
		condition, ok := c.(map[string]interface{})
		if !ok {
			continue
		}
		if condition["type"] == "Programmed" && condition["status"] == "True" {
			return true
		}
	}
	return false
}

// computeEndpoint returns the gateway service endpoint URL.
func (r *AIGatewayReconciler) computeEndpoint(aigw *gatewayv1alpha1.AIGateway) string {
	port := int32(8080)
	if len(aigw.Spec.Listeners) > 0 {
		port = aigw.Spec.Listeners[0].Port
	}
	return fmt.Sprintf("http://%s.%s.svc:%d", aigw.Name, aigw.Namespace, port)
}

// handleDeletion removes the finalizer after owned resources are garbage collected.
func (r *AIGatewayReconciler) handleDeletion(ctx context.Context, aigw *gatewayv1alpha1.AIGateway) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(aigw, AIGatewayFinalizer) {
		return ctrl.Result{}, nil
	}

	aiGatewayLogger.Info("Cleaning up AIGateway", "name", aigw.Name)

	if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		latest := &gatewayv1alpha1.AIGateway{}
		if err := r.Get(ctx, types.NamespacedName{
			Name:      aigw.Name,
			Namespace: aigw.Namespace,
		}, latest); err != nil {
			return err
		}
		controllerutil.RemoveFinalizer(latest, AIGatewayFinalizer)
		return r.Update(ctx, latest)
	}); err != nil {
		return ctrl.Result{}, err
	}

	aiGatewayLogger.Info("Removed finalizer from AIGateway", "name", aigw.Name)
	return ctrl.Result{}, nil
}

// setCondition updates a single condition on the AIGateway status.
func (r *AIGatewayReconciler) setCondition(ctx context.Context, aigw *gatewayv1alpha1.AIGateway, condType string, status metav1.ConditionStatus, reason, message string) {
	if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		latest := &gatewayv1alpha1.AIGateway{}
		if err := r.Get(ctx, types.NamespacedName{
			Name:      aigw.Name,
			Namespace: aigw.Namespace,
		}, latest); err != nil {
			return err
		}
		meta.SetStatusCondition(&latest.Status.Conditions, metav1.Condition{
			Type:               condType,
			Status:             status,
			Reason:             reason,
			Message:            message,
			ObservedGeneration: latest.Generation,
		})
		return r.Status().Update(ctx, latest)
	}); err != nil {
		aiGatewayLogger.Error(err, "Failed to update condition", "type", condType)
	}
}

// SetupWithManager sets up the controller with the Manager.
func (r *AIGatewayReconciler) SetupWithManager(mgr ctrl.Manager) error {
	b := ctrl.NewControllerManagedBy(mgr).
		For(&gatewayv1alpha1.AIGateway{})

	// Watch owned unstructured resources to react to their status changes.
	for _, gvk := range []schema.GroupVersionKind{gvkGateway, gvkAIServiceBackend, gvkAIGatewayRoute, gvkBackendSecurityPolicy} {
		u := &unstructured.Unstructured{}
		u.SetGroupVersionKind(gvk)
		b = b.WatchesRawSource(source.Kind(mgr.GetCache(), u, handler.TypedEnqueueRequestForOwner[*unstructured.Unstructured](
			mgr.GetScheme(), mgr.GetRESTMapper(), &gatewayv1alpha1.AIGateway{},
		)))
	}

	return b.Named("AIGateway").Complete(r)
}

// Naming helpers.

func backendName(aigwName, providerName string) string {
	return aigwName + "-" + providerName
}

func policyName(aigwName, providerName string) string {
	return aigwName + "-" + providerName + "-credentials"
}

// isOwnedBy checks whether an unstructured resource has an owner reference to the given AIGateway.
func isOwnedBy(obj *unstructured.Unstructured, aigw *gatewayv1alpha1.AIGateway) bool {
	for _, ref := range obj.GetOwnerReferences() {
		if ref.UID == aigw.UID {
			return true
		}
	}
	return false
}
