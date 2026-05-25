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
	"encoding/pem"
	"fmt"
	"math/big"
	"net/url"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
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
	"github.com/kagenti/operator/internal/spiffe"
)

const (
	AIGatewayFinalizer = "gateway.kagenti.dev/finalizer"

	// Envoy AI Gateway API group and versions.
	envoyAIGatewayGroup   = "aigateway.envoyproxy.io"
	envoyAIGatewayVersion = "v1alpha1"

	// Gateway API group and version.
	gatewayAPIGroup   = "gateway.networking.k8s.io"
	gatewayAPIVersion = "v1"

	// Envoy Gateway policy API group.
	envoyGatewayGroup   = "gateway.envoyproxy.io"
	envoyGatewayVersion = "v1alpha1"
)

var (
	aiGatewayLogger = ctrl.Log.WithName("controller").WithName("AIGateway")

	gvkGateway               = schema.GroupVersionKind{Group: gatewayAPIGroup, Version: gatewayAPIVersion, Kind: "Gateway"}
	gvkAIServiceBackend      = schema.GroupVersionKind{Group: envoyAIGatewayGroup, Version: envoyAIGatewayVersion, Kind: "AIServiceBackend"}
	gvkBackendSecurityPolicy = schema.GroupVersionKind{Group: envoyAIGatewayGroup, Version: envoyAIGatewayVersion, Kind: "BackendSecurityPolicy"}
	gvkAIGatewayRoute        = schema.GroupVersionKind{Group: envoyAIGatewayGroup, Version: envoyAIGatewayVersion, Kind: "AIGatewayRoute"}
	gvkClientTrafficPolicy   = schema.GroupVersionKind{Group: envoyGatewayGroup, Version: envoyGatewayVersion, Kind: "ClientTrafficPolicy"}
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
// +kubebuilder:rbac:groups=core,resources=secrets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=configmaps,verbs=get;list;watch
// +kubebuilder:rbac:groups=gateway.envoyproxy.io,resources=clienttrafficpolicies,verbs=get;list;watch;create;update;patch;delete

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

	if aigw.Spec.MTLS != nil {
		if err := r.reconcileMTLS(ctx, aigw); err != nil {
			r.setCondition(ctx, aigw, "Ready", metav1.ConditionFalse, "MTLSFailed", err.Error())
			return ctrl.Result{RequeueAfter: 30 * time.Second}, err
		}
	} else {
		// Clean up mTLS resources if mTLS was removed from spec.
		r.cleanupMTLSResources(ctx, aigw)
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

		// When mTLS is configured, switch listeners to HTTPS.
		if aigw.Spec.MTLS != nil {
			protocol = "HTTPS"
		}

		listener := map[string]interface{}{
			"name":     l.Name,
			"port":     int64(l.Port),
			"protocol": protocol,
		}

		// Add TLS config for HTTPS listeners when mTLS is configured.
		if aigw.Spec.MTLS != nil {
			certSecretName := serverCertSecretName(aigw.Name)
			if aigw.Spec.MTLS.ServerCertRef != nil {
				certSecretName = aigw.Spec.MTLS.ServerCertRef.Name
			}
			listener["tls"] = map[string]interface{}{
				"mode": "Terminate",
				"certificateRefs": []interface{}{
					map[string]interface{}{
						"kind": "Secret",
						"name": certSecretName,
					},
				},
			}
		}

		listeners = append(listeners, listener)
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

// reconcileMTLS creates/updates the trust bundle Secret, server certificate, and ClientTrafficPolicy.
func (r *AIGatewayReconciler) reconcileMTLS(ctx context.Context, aigw *gatewayv1alpha1.AIGateway) error {
	mtls := aigw.Spec.MTLS

	// 1. Sync trust bundle: read SPIFFE JSON ConfigMap → PEM Secret.
	cm := &corev1.ConfigMap{}
	cmKey := types.NamespacedName{
		Name:      mtls.TrustBundleConfigMap.Name,
		Namespace: mtls.TrustBundleConfigMap.Namespace,
	}
	if err := r.Get(ctx, cmKey, cm); err != nil {
		return fmt.Errorf("failed to get trust bundle configmap %s/%s: %w",
			cmKey.Namespace, cmKey.Name, err)
	}

	bundleKey := mtls.TrustBundleConfigMap.Key
	if bundleKey == "" {
		bundleKey = "bundle.spiffe"
	}
	raw, ok := cm.Data[bundleKey]
	if !ok || raw == "" {
		return fmt.Errorf("trust bundle configmap key %q not found or empty", bundleKey)
	}

	pemData, err := spiffe.ParseTrustBundleToPEM(raw)
	if err != nil {
		return fmt.Errorf("failed to convert trust bundle to PEM: %w", err)
	}

	caSecretName := caSecretName(aigw.Name)
	caSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      caSecretName,
			Namespace: aigw.Namespace,
		},
	}
	if err := r.Get(ctx, types.NamespacedName{Name: caSecretName, Namespace: aigw.Namespace}, caSecret); err != nil {
		if !apierrors.IsNotFound(err) {
			return err
		}
		// Create the CA secret.
		caSecret.Data = map[string][]byte{"ca.crt": pemData}
		if err := controllerutil.SetOwnerReference(aigw, caSecret, r.Scheme); err != nil {
			return fmt.Errorf("setting owner reference on CA secret: %w", err)
		}
		if err := r.Create(ctx, caSecret); err != nil {
			return fmt.Errorf("creating CA secret: %w", err)
		}
		aiGatewayLogger.Info("Created mTLS CA secret", "name", caSecretName)
	} else {
		// Update if content changed.
		caSecret.Data = map[string][]byte{"ca.crt": pemData}
		if err := r.Update(ctx, caSecret); err != nil {
			return fmt.Errorf("updating CA secret: %w", err)
		}
	}

	// 2. Ensure server certificate.
	if mtls.ServerCertRef == nil {
		if err := r.ensureSelfSignedCert(ctx, aigw); err != nil {
			return fmt.Errorf("ensuring self-signed server cert: %w", err)
		}
	}

	// 3. Create/update ClientTrafficPolicy.
	ctp := &unstructured.Unstructured{}
	ctp.SetGroupVersionKind(gvkClientTrafficPolicy)
	ctp.SetName(aigw.Name)
	ctp.SetNamespace(aigw.Namespace)

	desired := map[string]interface{}{
		"spec": map[string]interface{}{
			"targetRefs": []interface{}{
				map[string]interface{}{
					"group": gatewayAPIGroup,
					"kind":  "Gateway",
					"name":  aigw.Name,
				},
			},
			"tls": map[string]interface{}{
				"clientValidation": map[string]interface{}{
					"mode": "RequireAndVerify",
					"caCertificateRefs": []interface{}{
						map[string]interface{}{
							"kind":  "Secret",
							"group": "",
							"name":  caSecretName,
						},
					},
					"subjectAltNames": map[string]interface{}{
						"uris": []interface{}{
							map[string]interface{}{
								"type":  "Prefix",
								"value": fmt.Sprintf("spiffe://%s/", mtls.TrustDomain),
							},
						},
					},
				},
			},
		},
	}

	return r.createOrUpdate(ctx, aigw, ctp, desired)
}

// ensureSelfSignedCert creates a self-signed TLS certificate Secret for the Gateway
// if one doesn't already exist.
func (r *AIGatewayReconciler) ensureSelfSignedCert(ctx context.Context, aigw *gatewayv1alpha1.AIGateway) error {
	secretName := serverCertSecretName(aigw.Name)
	existing := &corev1.Secret{}
	err := r.Get(ctx, types.NamespacedName{Name: secretName, Namespace: aigw.Namespace}, existing)
	if err == nil {
		return nil // Already exists.
	}
	if !apierrors.IsNotFound(err) {
		return err
	}

	// Generate self-signed certificate.
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fmt.Errorf("generating private key: %w", err)
	}

	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			CommonName: fmt.Sprintf("%s.%s.svc", aigw.Name, aigw.Namespace),
		},
		DNSNames: []string{
			aigw.Name,
			fmt.Sprintf("%s.%s", aigw.Name, aigw.Namespace),
			fmt.Sprintf("%s.%s.svc", aigw.Name, aigw.Namespace),
		},
		NotBefore: time.Now().Add(-time.Hour),
		NotAfter:  time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:  x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage: []x509.ExtKeyUsage{
			x509.ExtKeyUsageServerAuth,
		},
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return fmt.Errorf("creating self-signed certificate: %w", err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return fmt.Errorf("marshaling private key: %w", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: aigw.Namespace,
		},
		Type: corev1.SecretTypeTLS,
		Data: map[string][]byte{
			"tls.crt": certPEM,
			"tls.key": keyPEM,
		},
	}
	if err := controllerutil.SetOwnerReference(aigw, secret, r.Scheme); err != nil {
		return fmt.Errorf("setting owner reference on server cert secret: %w", err)
	}
	if err := r.Create(ctx, secret); err != nil {
		return fmt.Errorf("creating server cert secret: %w", err)
	}
	aiGatewayLogger.Info("Created self-signed server certificate", "name", secretName)
	return nil
}

// cleanupMTLSResources removes mTLS-related resources (CA Secret, server cert Secret,
// ClientTrafficPolicy) when mTLS is removed from the spec.
func (r *AIGatewayReconciler) cleanupMTLSResources(ctx context.Context, aigw *gatewayv1alpha1.AIGateway) {
	// Delete ClientTrafficPolicy.
	ctp := &unstructured.Unstructured{}
	ctp.SetGroupVersionKind(gvkClientTrafficPolicy)
	ctp.SetName(aigw.Name)
	ctp.SetNamespace(aigw.Namespace)
	if err := r.Delete(ctx, ctp); err != nil && !apierrors.IsNotFound(err) {
		aiGatewayLogger.Error(err, "Failed to delete ClientTrafficPolicy", "name", aigw.Name)
	}

	// Delete CA Secret.
	caSecret := &corev1.Secret{}
	caName := caSecretName(aigw.Name)
	if err := r.Get(ctx, types.NamespacedName{Name: caName, Namespace: aigw.Namespace}, caSecret); err == nil {
		if isOwnedByAIGateway(caSecret, aigw) {
			if err := r.Delete(ctx, caSecret); err != nil && !apierrors.IsNotFound(err) {
				aiGatewayLogger.Error(err, "Failed to delete CA secret", "name", caName)
			}
		}
	}

	// Delete self-signed server cert Secret.
	serverSecret := &corev1.Secret{}
	serverName := serverCertSecretName(aigw.Name)
	if err := r.Get(ctx, types.NamespacedName{Name: serverName, Namespace: aigw.Namespace}, serverSecret); err == nil {
		if isOwnedByAIGateway(serverSecret, aigw) {
			if err := r.Delete(ctx, serverSecret); err != nil && !apierrors.IsNotFound(err) {
				aiGatewayLogger.Error(err, "Failed to delete server cert secret", "name", serverName)
			}
		}
	}
}

// isOwnedByAIGateway checks whether a typed resource has an owner reference to the given AIGateway.
func isOwnedByAIGateway(obj client.Object, aigw *gatewayv1alpha1.AIGateway) bool {
	for _, ref := range obj.GetOwnerReferences() {
		if ref.UID == aigw.UID {
			return true
		}
	}
	return false
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
	scheme := "http"
	if aigw.Spec.MTLS != nil {
		scheme = "https"
	}
	return fmt.Sprintf("%s://%s.%s.svc:%d", scheme, aigw.Name, aigw.Namespace, port)
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
	for _, gvk := range []schema.GroupVersionKind{gvkGateway, gvkAIServiceBackend, gvkAIGatewayRoute, gvkBackendSecurityPolicy, gvkClientTrafficPolicy} {
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

func caSecretName(aigwName string) string {
	return aigwName + "-mtls-ca"
}

func serverCertSecretName(aigwName string) string {
	return aigwName + "-mtls-server"
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
