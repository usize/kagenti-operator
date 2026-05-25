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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// AIGatewaySpec defines the desired state of AIGateway.
type AIGatewaySpec struct {
	// GatewayClassName is the name of the GatewayClass (e.g. "eg" for Envoy Gateway).
	// +kubebuilder:validation:MinLength=1
	GatewayClassName string `json:"gatewayClassName"`

	// Listeners defines the Gateway API listener configuration.
	// +kubebuilder:validation:MinItems=1
	Listeners []AIGatewayListener `json:"listeners"`

	// Providers defines the LLM providers to route to.
	// +kubebuilder:validation:MinItems=1
	Providers []AIGatewayProvider `json:"providers"`

	// Service configures the generated Gateway service.
	// +optional
	Service *AIGatewayService `json:"service,omitempty"`

	// MTLS configures mutual TLS access control for the gateway.
	// When set, only clients presenting a valid X.509-SVID from the
	// configured SPIRE trust domain are allowed to connect.
	// +optional
	MTLS *AIGatewayMTLS `json:"mtls,omitempty"`
}

// AIGatewayListener defines a listener on the Gateway.
type AIGatewayListener struct {
	// Name is the listener name.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// Port is the network port.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	Port int32 `json:"port"`

	// Protocol is the listener protocol (HTTP or HTTPS).
	// +kubebuilder:validation:Enum=HTTP;HTTPS
	// +kubebuilder:default=HTTP
	Protocol string `json:"protocol,omitempty"`
}

// AIGatewayProvider defines an LLM provider backend.
type AIGatewayProvider struct {
	// Name is a unique identifier for this provider (becomes AIServiceBackend name).
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([a-z0-9\-]*[a-z0-9])?$`
	Name string `json:"name"`

	// Endpoint is the provider API base URL or in-cluster service address.
	// +kubebuilder:validation:MinLength=1
	Endpoint string `json:"endpoint"`

	// Schema is the API schema used by this provider.
	// +kubebuilder:validation:Enum=OpenAI;Anthropic;AWSBedrock;AzureOpenAI;GoogleGenAI
	Schema string `json:"schema"`

	// CredentialRef references a Secret containing the API key.
	// Omit for unauthenticated providers (e.g. local Ollama).
	// +optional
	CredentialRef *CredentialReference `json:"credentialRef,omitempty"`

	// CredentialKey is the key within the Secret (default "api-key").
	// +optional
	// +kubebuilder:default="api-key"
	CredentialKey string `json:"credentialKey,omitempty"`

	// Models lists the model identifiers available from this provider.
	// +kubebuilder:validation:MinItems=1
	Models []string `json:"models"`
}

// CredentialReference references a Secret in the same namespace.
type CredentialReference struct {
	// Name is the Secret name.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
}

// AIGatewayService configures the generated Gateway service.
type AIGatewayService struct {
	// Type is the Kubernetes Service type.
	// +kubebuilder:validation:Enum=ClusterIP;LoadBalancer;NodePort
	// +kubebuilder:default=ClusterIP
	// +optional
	Type string `json:"type,omitempty"`
}

// AIGatewayMTLS configures mutual TLS access control using SPIRE SVIDs.
type AIGatewayMTLS struct {
	// TrustDomain is the SPIRE trust domain (e.g. "example.org").
	// Only SVIDs with spiffe://<trustDomain>/... URI SANs are accepted.
	// +kubebuilder:validation:MinLength=1
	TrustDomain string `json:"trustDomain"`

	// TrustBundleConfigMap references the ConfigMap containing the SPIRE
	// trust bundle in SPIFFE JSON format.
	TrustBundleConfigMap TrustBundleRef `json:"trustBundleConfigMap"`

	// ServerCertRef optionally references a Secret containing the gateway's
	// TLS serving certificate (tls.crt + tls.key). If omitted, the
	// controller generates a self-signed certificate.
	// +optional
	ServerCertRef *CertificateReference `json:"serverCertRef,omitempty"`
}

// TrustBundleRef references a ConfigMap containing a SPIFFE trust bundle.
type TrustBundleRef struct {
	// Name of the ConfigMap.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// Namespace of the ConfigMap.
	// +kubebuilder:validation:MinLength=1
	Namespace string `json:"namespace"`

	// Key within the ConfigMap containing the SPIFFE JSON bundle.
	// +kubebuilder:default="bundle.spiffe"
	// +optional
	Key string `json:"key,omitempty"`
}

// CertificateReference references a Secret containing TLS certificates.
type CertificateReference struct {
	// Name is the Secret name containing tls.crt and tls.key.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
}

// AIGatewayStatus defines the observed state of AIGateway.
type AIGatewayStatus struct {
	// Conditions represent the current state of the AIGateway.
	// Known condition types: Ready, GatewayReady, RoutesConfigured.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// Endpoint is the Gateway service endpoint URL.
	// +optional
	Endpoint string `json:"endpoint,omitempty"`

	// ObservedGeneration is the last generation reconciled.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// ConfiguredModels is the count of model routes configured.
	// +optional
	ConfiguredModels int32 `json:"configuredModels,omitempty"`

	// ConfiguredProviders is the count of backends created.
	// +optional
	ConfiguredProviders int32 `json:"configuredProviders,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=aigw
// +kubebuilder:printcolumn:name="Providers",type="integer",JSONPath=".status.configuredProviders",description="Number of configured providers"
// +kubebuilder:printcolumn:name="Models",type="integer",JSONPath=".status.configuredModels",description="Number of configured model routes"
// +kubebuilder:printcolumn:name="Endpoint",type="string",JSONPath=".status.endpoint",description="Gateway endpoint"
// +kubebuilder:printcolumn:name="Ready",type="string",JSONPath=".status.conditions[?(@.type=='Ready')].status",description="Ready status"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// AIGateway is the Schema for the aigateways API.
type AIGateway struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   AIGatewaySpec   `json:"spec,omitempty"`
	Status AIGatewayStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// AIGatewayList contains a list of AIGateway.
type AIGatewayList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []AIGateway `json:"items"`
}

func init() {
	SchemeBuilder.Register(&AIGateway{}, &AIGatewayList{})
}
