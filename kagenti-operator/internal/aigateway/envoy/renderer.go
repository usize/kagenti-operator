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

// Package envoy renders data-plane-agnostic intent types into Envoy AI Gateway resources.
package envoy

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
	gwapiv1a2 "sigs.k8s.io/gateway-api/apis/v1alpha2"

	aigwv1a1 "github.com/envoyproxy/ai-gateway/api/v1alpha1"
	egv1a1 "github.com/envoyproxy/gateway/api/v1alpha1"

	"github.com/kagenti/operator/internal/aigateway"
)

// ResourceNamePrefix returns a deterministic name for generated resources
// based on the policy name and a component identifier.
func ResourceNamePrefix(policyName, component string) string {
	return fmt.Sprintf("%s-%s", policyName, component)
}

// RenderRouting translates a RoutingIntent into Envoy AI Gateway resources.
// Returns: Backend, AIServiceBackend, BackendSecurityPolicy (per provider),
// AIGatewayRoute (one total), and BackendTrafficPolicy (if rate limits exist).
func RenderRouting(intent *aigateway.RoutingIntent) []client.Object {
	var objects []client.Object

	// Per-provider resources
	for _, p := range intent.Providers {
		objects = append(objects, renderBackend(intent, &p))
		objects = append(objects, renderAIServiceBackend(intent, &p))
		if p.Credentials != nil {
			objects = append(objects, renderBackendSecurityPolicy(intent, &p))
		}
	}

	// One AIGatewayRoute for all models
	objects = append(objects, renderAIGatewayRoute(intent))

	// One BackendTrafficPolicy if any model has rate limiting
	if btp := renderBackendTrafficPolicy(intent); btp != nil {
		objects = append(objects, btp)
	}

	return objects
}

// RenderAccess translates an AccessIntent into Envoy Gateway resources.
// Returns: CA Secret, optionally a server cert Secret, and a ClientTrafficPolicy.
func RenderAccess(intent *aigateway.AccessIntent) ([]client.Object, error) {
	var objects []client.Object

	// CA Secret from trust bundle
	caSecretName := fmt.Sprintf("%s-mtls-ca", intent.PolicyName)
	caSecret := &corev1.Secret{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "Secret",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      caSecretName,
			Namespace: intent.PolicyNamespace,
		},
		Data: map[string][]byte{
			"ca.crt": intent.CACertPEM,
		},
	}
	objects = append(objects, caSecret)

	// Server cert (self-signed if no serverCertRef)
	serverCertSecretName := intent.ServerCertRef
	if serverCertSecretName == "" {
		serverCertSecretName = fmt.Sprintf("%s-mtls-server", intent.PolicyName)
		certPEM, keyPEM, err := GenerateSelfSignedCert(intent.GatewayName)
		if err != nil {
			return nil, fmt.Errorf("generating self-signed server cert: %w", err)
		}
		serverSecret := &corev1.Secret{
			TypeMeta: metav1.TypeMeta{
				APIVersion: "v1",
				Kind:       "Secret",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name:      serverCertSecretName,
				Namespace: intent.PolicyNamespace,
			},
			Type: corev1.SecretTypeTLS,
			Data: map[string][]byte{
				"tls.crt": certPEM,
				"tls.key": keyPEM,
			},
		}
		objects = append(objects, serverSecret)
	}

	// ClientTrafficPolicy
	ctp := renderClientTrafficPolicy(intent, caSecretName)
	objects = append(objects, ctp)

	return objects, nil
}

func renderBackend(intent *aigateway.RoutingIntent, p *aigateway.ProviderIntent) *egv1a1.Backend {
	return &egv1a1.Backend{
		TypeMeta: metav1.TypeMeta{
			APIVersion: egv1a1.GroupVersion.String(),
			Kind:       "Backend",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      ResourceNamePrefix(intent.PolicyName, p.Name),
			Namespace: intent.PolicyNamespace,
		},
		Spec: egv1a1.BackendSpec{
			Endpoints: []egv1a1.BackendEndpoint{
				{
					FQDN: &egv1a1.FQDNEndpoint{
						Hostname: p.Host,
						Port:     p.Port,
					},
				},
			},
		},
	}
}

func renderAIServiceBackend(intent *aigateway.RoutingIntent, p *aigateway.ProviderIntent) *aigwv1a1.AIServiceBackend {
	backendName := ResourceNamePrefix(intent.PolicyName, p.Name)
	egGroup := gwapiv1.Group(egv1a1.GroupName)
	egKind := gwapiv1.Kind("Backend")
	port := gwapiv1.PortNumber(p.Port)

	return &aigwv1a1.AIServiceBackend{
		TypeMeta: metav1.TypeMeta{
			APIVersion: aigwv1a1.SchemeGroupVersion.String(),
			Kind:       "AIServiceBackend",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      backendName,
			Namespace: intent.PolicyNamespace,
		},
		Spec: aigwv1a1.AIServiceBackendSpec{
			APISchema: aigwv1a1.VersionedAPISchema{
				Name: aigwv1a1.APISchema(p.Schema),
			},
			BackendRef: gwapiv1.BackendObjectReference{
				Group: &egGroup,
				Kind:  &egKind,
				Name:  gwapiv1.ObjectName(backendName),
				Port:  &port,
			},
		},
	}
}

func renderBackendSecurityPolicy(intent *aigateway.RoutingIntent, p *aigateway.ProviderIntent) *aigwv1a1.BackendSecurityPolicy {
	bspName := ResourceNamePrefix(intent.PolicyName, p.Name)
	cred := p.Credentials

	aiSBGroup := gwapiv1a2.Group(aigwv1a1.GroupName)
	aiSBKind := gwapiv1a2.Kind("AIServiceBackend")

	bsp := &aigwv1a1.BackendSecurityPolicy{
		TypeMeta: metav1.TypeMeta{
			APIVersion: aigwv1a1.SchemeGroupVersion.String(),
			Kind:       "BackendSecurityPolicy",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      bspName,
			Namespace: intent.PolicyNamespace,
		},
		Spec: aigwv1a1.BackendSecurityPolicySpec{
			TargetRefs: []gwapiv1a2.LocalPolicyTargetReference{
				{
					Group: aiSBGroup,
					Kind:  aiSBKind,
					Name:  gwapiv1a2.ObjectName(bspName),
				},
			},
		},
	}

	switch cred.Type {
	case "APIKey":
		bsp.Spec.Type = aigwv1a1.BackendSecurityPolicyTypeAPIKey
		secretName := gwapiv1.ObjectName(cred.SecretName)
		bsp.Spec.APIKey = &aigwv1a1.BackendSecurityPolicyAPIKey{
			SecretRef: &gwapiv1.SecretObjectReference{
				Name: secretName,
			},
		}
	case "AWSCredentials":
		bsp.Spec.Type = aigwv1a1.BackendSecurityPolicyTypeAWSCredentials
		bsp.Spec.AWSCredentials = &aigwv1a1.BackendSecurityPolicyAWSCredentials{
			Region: cred.Region,
		}
	}

	return bsp
}

func renderAIGatewayRoute(intent *aigateway.RoutingIntent) *aigwv1a1.AIGatewayRoute {
	gwNamespace := gwapiv1.Namespace(intent.PolicyNamespace)

	var rules []aigwv1a1.AIGatewayRouteRule
	for _, m := range intent.Models {
		var backendRefs []aigwv1a1.AIGatewayRouteRuleBackendRef
		for _, b := range m.Backends {
			ref := aigwv1a1.AIGatewayRouteRuleBackendRef{
				Name: ResourceNamePrefix(intent.PolicyName, b.ProviderName),
			}
			if b.ModelName != "" {
				ref.ModelNameOverride = b.ModelName
			}
			if b.Priority > 0 {
				p := uint32(b.Priority)
				ref.Priority = &p
			}
			backendRefs = append(backendRefs, ref)
		}

		rule := aigwv1a1.AIGatewayRouteRule{
			BackendRefs: backendRefs,
			Matches: []aigwv1a1.AIGatewayRouteRuleMatch{
				{
					Headers: []gwapiv1.HTTPHeaderMatch{
						{
							Name:  "x-ai-eg-model",
							Value: m.Name,
						},
					},
				},
			},
		}
		rules = append(rules, rule)
	}

	route := &aigwv1a1.AIGatewayRoute{
		TypeMeta: metav1.TypeMeta{
			APIVersion: aigwv1a1.SchemeGroupVersion.String(),
			Kind:       "AIGatewayRoute",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      intent.PolicyName,
			Namespace: intent.PolicyNamespace,
		},
		Spec: aigwv1a1.AIGatewayRouteSpec{
			ParentRefs: []gwapiv1.ParentReference{
				{
					Name:      gwapiv1.ObjectName(intent.GatewayName),
					Namespace: &gwNamespace,
				},
			},
			Rules: rules,
		},
	}

	// Add LLMRequestCosts if any model has rate limiting
	for _, m := range intent.Models {
		if m.RateLimit != nil {
			route.Spec.LLMRequestCosts = []aigwv1a1.LLMRequestCost{
				{
					MetadataKey: "input_token",
					Type:        aigwv1a1.LLMRequestCostTypeInputToken,
				},
				{
					MetadataKey: "output_token",
					Type:        aigwv1a1.LLMRequestCostTypeOutputToken,
				},
				{
					MetadataKey: "total_token",
					Type:        aigwv1a1.LLMRequestCostTypeTotalToken,
				},
			}
			break
		}
	}

	return route
}

func renderBackendTrafficPolicy(intent *aigateway.RoutingIntent) *egv1a1.BackendTrafficPolicy {
	hasRateLimit := false
	for _, m := range intent.Models {
		if m.RateLimit != nil && m.RateLimit.RequestsPerMinute != nil {
			hasRateLimit = true
			break
		}
	}
	if !hasRateLimit {
		return nil
	}

	gwGroup := gwapiv1.Group("gateway.networking.k8s.io")
	gwKind := gwapiv1.Kind("Gateway")

	btp := &egv1a1.BackendTrafficPolicy{
		TypeMeta: metav1.TypeMeta{
			APIVersion: egv1a1.GroupVersion.String(),
			Kind:       "BackendTrafficPolicy",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      intent.PolicyName,
			Namespace: intent.PolicyNamespace,
		},
		Spec: egv1a1.BackendTrafficPolicySpec{
			PolicyTargetReferences: egv1a1.PolicyTargetReferences{
				TargetRefs: []gwapiv1.LocalPolicyTargetReferenceWithSectionName{
					{
						LocalPolicyTargetReference: gwapiv1.LocalPolicyTargetReference{
							Group: gwGroup,
							Kind:  gwKind,
							Name:  gwapiv1.ObjectName(intent.GatewayName),
						},
					},
				},
			},
		},
	}

	// Local rate limiting: build rules per model
	var rules []egv1a1.RateLimitRule
	for _, m := range intent.Models {
		if m.RateLimit == nil || m.RateLimit.RequestsPerMinute == nil {
			continue
		}
		modelName := m.Name
		rule := egv1a1.RateLimitRule{
			ClientSelectors: []egv1a1.RateLimitSelectCondition{
				{
					Headers: []egv1a1.HeaderMatch{
						{
							Name:  "x-ai-eg-model",
							Value: &modelName,
						},
					},
				},
			},
			Limit: egv1a1.RateLimitValue{
				Requests: uint32(*m.RateLimit.RequestsPerMinute),
				Unit:     egv1a1.RateLimitUnit("Minute"),
			},
		}
		rules = append(rules, rule)
	}

	if len(rules) > 0 {
		btp.Spec.RateLimit = &egv1a1.RateLimitSpec{
			Local: &egv1a1.LocalRateLimit{
				Rules: rules,
			},
		}
	}

	return btp
}

func renderClientTrafficPolicy(intent *aigateway.AccessIntent, caSecretName string) *egv1a1.ClientTrafficPolicy {
	gwGroup := gwapiv1.Group("gateway.networking.k8s.io")
	gwKind := gwapiv1.Kind("Gateway")

	return &egv1a1.ClientTrafficPolicy{
		TypeMeta: metav1.TypeMeta{
			APIVersion: egv1a1.GroupVersion.String(),
			Kind:       "ClientTrafficPolicy",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      intent.PolicyName,
			Namespace: intent.PolicyNamespace,
		},
		Spec: egv1a1.ClientTrafficPolicySpec{
			PolicyTargetReferences: egv1a1.PolicyTargetReferences{
				TargetRefs: []gwapiv1.LocalPolicyTargetReferenceWithSectionName{
					{
						LocalPolicyTargetReference: gwapiv1.LocalPolicyTargetReference{
							Group: gwGroup,
							Kind:  gwKind,
							Name:  gwapiv1.ObjectName(intent.GatewayName),
						},
					},
				},
			},
			TLS: &egv1a1.ClientTLSSettings{
				ClientValidation: &egv1a1.ClientValidationContext{
					CACertificateRefs: []gwapiv1.SecretObjectReference{
						{
							Name: gwapiv1.ObjectName(caSecretName),
						},
					},
				},
			},
		},
	}
}
