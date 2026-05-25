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

package v1alpha1

import (
	"context"
	"fmt"
	"strings"

	gatewayv1alpha1 "github.com/kagenti/operator/api/gateway/v1alpha1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

var aigatewaylog = ctrl.Log.WithName("aigateway-webhook")

func SetupAIGatewayWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).
		For(&gatewayv1alpha1.AIGateway{}).
		WithValidator(&AIGatewayValidator{}).
		Complete()
}

//+kubebuilder:webhook:path=/validate-gateway-kagenti-dev-v1alpha1-aigateway,mutating=false,failurePolicy=fail,sideEffects=None,groups=gateway.kagenti.dev,resources=aigateways,verbs=create;update,versions=v1alpha1,name=vaigateway.kb.io,admissionReviewVersions=v1

type AIGatewayValidator struct{}

var validSchemas = map[string]bool{
	"OpenAI":      true,
	"Anthropic":   true,
	"AWSBedrock":  true,
	"AzureOpenAI": true,
	"GoogleGenAI": true,
}

func (v *AIGatewayValidator) ValidateCreate(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	aigw, ok := obj.(*gatewayv1alpha1.AIGateway)
	if !ok {
		return nil, fmt.Errorf("expected an AIGateway but got a %T", obj)
	}

	aigatewaylog.Info("validate create", "name", aigw.Name)

	return v.validateAIGateway(aigw)
}

func (v *AIGatewayValidator) ValidateUpdate(ctx context.Context, oldObj, newObj runtime.Object) (admission.Warnings, error) {
	aigw, ok := newObj.(*gatewayv1alpha1.AIGateway)
	if !ok {
		return nil, fmt.Errorf("expected an AIGateway but got a %T", newObj)
	}

	aigatewaylog.Info("validate update", "name", aigw.Name)

	return v.validateAIGateway(aigw)
}

func (v *AIGatewayValidator) ValidateDelete(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	aigw, ok := obj.(*gatewayv1alpha1.AIGateway)
	if !ok {
		return nil, fmt.Errorf("expected an AIGateway but got a %T", obj)
	}

	aigatewaylog.Info("validate delete", "name", aigw.Name)

	return nil, nil
}

func (v *AIGatewayValidator) validateAIGateway(aigw *gatewayv1alpha1.AIGateway) (admission.Warnings, error) {
	var errs []string

	if aigw.Spec.GatewayClassName == "" {
		errs = append(errs, "spec.gatewayClassName is required")
	}

	if len(aigw.Spec.Listeners) == 0 {
		errs = append(errs, "at least one listener is required in spec.listeners")
	}

	if len(aigw.Spec.Providers) == 0 {
		errs = append(errs, "at least one provider is required in spec.providers")
	}

	// Check provider-level validations.
	providerNames := make(map[string]bool)
	for i, p := range aigw.Spec.Providers {
		if providerNames[p.Name] {
			errs = append(errs, fmt.Sprintf("spec.providers[%d]: duplicate provider name %q", i, p.Name))
		}
		providerNames[p.Name] = true

		if p.Endpoint == "" {
			errs = append(errs, fmt.Sprintf("spec.providers[%d]: endpoint is required", i))
		}

		if len(p.Models) == 0 {
			errs = append(errs, fmt.Sprintf("spec.providers[%d]: at least one model is required", i))
		}

		if !validSchemas[p.Schema] {
			errs = append(errs, fmt.Sprintf("spec.providers[%d]: invalid schema %q, must be one of: OpenAI, Anthropic, AWSBedrock, AzureOpenAI, GoogleGenAI", i, p.Schema))
		}

		if p.CredentialRef != nil && p.CredentialRef.Name == "" {
			errs = append(errs, fmt.Sprintf("spec.providers[%d]: credentialRef.name must be non-empty when credentialRef is specified", i))
		}
	}

	// Validate mTLS fields.
	if aigw.Spec.MTLS != nil {
		if aigw.Spec.MTLS.TrustDomain == "" {
			errs = append(errs, "spec.mtls.trustDomain is required when mtls is configured")
		}
		if aigw.Spec.MTLS.TrustBundleConfigMap.Name == "" {
			errs = append(errs, "spec.mtls.trustBundleConfigMap.name is required")
		}
		if aigw.Spec.MTLS.TrustBundleConfigMap.Namespace == "" {
			errs = append(errs, "spec.mtls.trustBundleConfigMap.namespace is required")
		}
		if aigw.Spec.MTLS.ServerCertRef != nil && aigw.Spec.MTLS.ServerCertRef.Name == "" {
			errs = append(errs, "spec.mtls.serverCertRef.name must be non-empty when serverCertRef is specified")
		}
	}

	if len(errs) > 0 {
		return nil, fmt.Errorf("validation failed: %s", strings.Join(errs, "; "))
	}

	return nil, nil
}
