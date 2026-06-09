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

package v1alpha1

// TargetRef identifies the Gateway this policy attaches to.
type TargetRef struct {
	// Group is the API group of the target resource.
	// +kubebuilder:default="gateway.networking.k8s.io"
	Group string `json:"group"`

	// Kind is the kind of the target resource.
	// +kubebuilder:default="Gateway"
	Kind string `json:"kind"`

	// Name is the name of the target resource.
	Name string `json:"name"`
}

// ProviderSpec defines an LLM provider's connection configuration.
type ProviderSpec struct {
	// Name is a unique identifier for the provider within this policy.
	Name string `json:"name"`

	// Endpoint is the base URL for the provider's API.
	Endpoint string `json:"endpoint"`

	// Schema is the API schema the provider uses (e.g. OpenAI, AWSBedrock).
	Schema string `json:"schema"`

	// Credentials defines the authentication method for the provider.
	// +optional
	Credentials *CredentialSpec `json:"credentials,omitempty"`
}

// CredentialSpec defines authentication credentials for a provider.
type CredentialSpec struct {
	// Type is the credential type: APIKey, AWSCredentials, AzureCredentials, GCPCredentials.
	Type string `json:"type"`

	// SecretRef references a Secret containing the API key.
	// Used when Type is APIKey.
	// +optional
	SecretRef *SecretKeyRef `json:"secretRef,omitempty"`

	// Region is the AWS region. Used when Type is AWSCredentials.
	// +optional
	Region string `json:"region,omitempty"`

	// TenantId is the Azure tenant ID. Used when Type is AzureCredentials.
	// +optional
	TenantId string `json:"tenantId,omitempty"`

	// ClientId is the Azure client ID. Used when Type is AzureCredentials.
	// +optional
	ClientId string `json:"clientId,omitempty"`

	// ClientSecretRef references a Secret containing the Azure client secret.
	// Used when Type is AzureCredentials.
	// +optional
	ClientSecretRef *SecretKeyRef `json:"clientSecretRef,omitempty"`
}

// SecretKeyRef references a key within a Secret.
type SecretKeyRef struct {
	// Name is the name of the Secret.
	Name string `json:"name"`

	// Key is the key within the Secret.
	Key string `json:"key"`
}

// ModelSpec defines a client-facing model with backend routing.
type ModelSpec struct {
	// Name is the virtual model name that clients request.
	Name string `json:"name"`

	// Backends lists the provider backends that serve this model.
	Backends []ModelBackend `json:"backends"`

	// RateLimit defines per-model rate limiting.
	// +optional
	RateLimit *RateLimitSpec `json:"rateLimit,omitempty"`

	// Failover defines per-model failover configuration.
	// +optional
	Failover *FailoverSpec `json:"failover,omitempty"`
}

// ModelBackend maps a model to a specific provider and actual model name.
type ModelBackend struct {
	// Provider references a provider by name from the providers list.
	Provider string `json:"provider"`

	// Model is the actual model name at the provider.
	Model string `json:"model"`

	// Priority controls failover order (lowest = highest priority).
	// +optional
	// +kubebuilder:default=0
	Priority int `json:"priority,omitempty"`
}

// RateLimitSpec defines rate limiting for a model.
type RateLimitSpec struct {
	// RequestsPerMinute is the maximum number of requests per minute.
	// +optional
	RequestsPerMinute *int `json:"requestsPerMinute,omitempty"`

	// TokensPerHour is the maximum number of tokens per hour.
	// +optional
	TokensPerHour *int64 `json:"tokensPerHour,omitempty"`

	// TokenCountMode determines which tokens to count: InputToken, OutputToken, TotalToken.
	// +optional
	// +kubebuilder:default="TotalToken"
	TokenCountMode string `json:"tokenCountMode,omitempty"`
}

// FailoverSpec defines failover behavior for a model.
type FailoverSpec struct {
	// RetryOn lists HTTP status codes that trigger a retry.
	// +optional
	RetryOn []int `json:"retryOn,omitempty"`

	// MaxRetries is the maximum number of retry attempts.
	// +optional
	// +kubebuilder:default=1
	MaxRetries int `json:"maxRetries,omitempty"`
}

// MTLSSpec defines mTLS configuration for gateway access control.
type MTLSSpec struct {
	// TrustDomain is the SPIFFE trust domain for client certificate validation.
	TrustDomain string `json:"trustDomain"`

	// TrustBundleConfigMap references the ConfigMap containing the SPIFFE trust bundle.
	TrustBundleConfigMap ConfigMapRef `json:"trustBundleConfigMap"`

	// ServerCertRef references an existing TLS Secret for the gateway's server certificate.
	// If omitted, a self-signed certificate is generated.
	// +optional
	ServerCertRef *SecretRef `json:"serverCertRef,omitempty"`
}

// ConfigMapRef references a key within a ConfigMap.
type ConfigMapRef struct {
	// Name is the name of the ConfigMap.
	Name string `json:"name"`

	// Namespace is the namespace of the ConfigMap.
	// +optional
	Namespace string `json:"namespace,omitempty"`

	// Key is the key within the ConfigMap. Defaults to "bundle.spiffe".
	// +optional
	// +kubebuilder:default="bundle.spiffe"
	Key string `json:"key,omitempty"`
}

// SecretRef references a Secret by name.
type SecretRef struct {
	// Name is the name of the Secret.
	Name string `json:"name"`
}
