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

// Package aigateway defines data-plane-agnostic intent types for AI Gateway
// routing and access control. These are translated by a renderer into
// data-plane-specific resources (e.g. Envoy AI Gateway CRDs).
package aigateway

// RoutingIntent is a data-plane-agnostic representation of the routing
// configuration declared in an AIRoutingPolicy.
type RoutingIntent struct {
	// PolicyName is the name of the AIRoutingPolicy that owns this intent.
	PolicyName string

	// PolicyNamespace is the namespace of the AIRoutingPolicy.
	PolicyNamespace string

	// GatewayName is the name of the target Gateway.
	GatewayName string

	// Providers lists the configured LLM provider backends.
	Providers []ProviderIntent

	// Models lists the client-facing model definitions.
	Models []ModelIntent
}

// ProviderIntent represents a single LLM provider backend.
type ProviderIntent struct {
	// Name is the unique provider identifier.
	Name string

	// Host is the provider endpoint hostname.
	Host string

	// Port is the provider endpoint port.
	Port int32

	// Schema is the API schema (e.g. "OpenAI", "AWSBedrock").
	Schema string

	// Credentials holds optional authentication configuration.
	Credentials *CredentialIntent
}

// CredentialIntent represents provider authentication.
type CredentialIntent struct {
	// Type is the credential type: APIKey, AWSCredentials, AzureCredentials.
	Type string

	// SecretName is the Secret name (for APIKey type).
	SecretName string

	// SecretKey is the key within the Secret (for APIKey type).
	SecretKey string

	// Region is the AWS region (for AWSCredentials type).
	Region string

	// TenantId is the Azure tenant ID (for AzureCredentials type).
	TenantId string

	// ClientId is the Azure client ID (for AzureCredentials type).
	ClientId string

	// ClientSecretName is the Secret name for Azure client secret.
	ClientSecretName string

	// ClientSecretKey is the key within the Azure client secret Secret.
	ClientSecretKey string
}

// ModelIntent represents a client-facing model with backend routing.
type ModelIntent struct {
	// Name is the virtual model name that clients request.
	Name string

	// Backends lists the provider backends for this model.
	Backends []ModelBackendIntent

	// RateLimit defines optional per-model rate limiting.
	RateLimit *RateLimitIntent

	// Failover defines optional per-model retry configuration.
	Failover *FailoverIntent
}

// ModelBackendIntent maps a model to a specific provider backend.
type ModelBackendIntent struct {
	// ProviderName references a ProviderIntent by name.
	ProviderName string

	// ModelName is the actual model name at the provider.
	ModelName string

	// Priority controls failover order (lowest = highest priority).
	Priority int
}

// RateLimitIntent defines rate limiting for a model.
type RateLimitIntent struct {
	// RequestsPerMinute is the max requests per minute (local rate limit).
	RequestsPerMinute *int
}

// FailoverIntent defines retry behavior.
type FailoverIntent struct {
	// RetryOn lists HTTP status codes that trigger a retry.
	RetryOn []int

	// MaxRetries is the max number of retry attempts.
	MaxRetries int
}

// AccessIntent is a data-plane-agnostic representation of the access
// control configuration declared in an AIAccessPolicy.
type AccessIntent struct {
	// PolicyName is the name of the AIAccessPolicy that owns this intent.
	PolicyName string

	// PolicyNamespace is the namespace of the AIAccessPolicy.
	PolicyNamespace string

	// GatewayName is the name of the target Gateway.
	GatewayName string

	// TrustDomain is the SPIFFE trust domain.
	TrustDomain string

	// CACertPEM is the PEM-encoded CA certificate bundle.
	CACertPEM []byte

	// ServerCertRef is the name of an existing server cert Secret.
	// If empty, a self-signed certificate should be generated.
	ServerCertRef string
}
