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

// AgentCardSpec defines the desired state of AgentCard.
type AgentCardSpec struct {
	// SyncPeriod is how often to re-fetch the agent card (e.g., "30s", "5m")
	// +optional
	// +kubebuilder:default="30s"
	SyncPeriod string `json:"syncPeriod,omitempty"`

	// TargetRef identifies the workload backing this agent using duck typing.
	// The referenced workload must have the required Kagenti labels (kagenti.io/type=agent).
	// This is the preferred way to reference agent workloads.
	// +optional
	TargetRef *TargetRef `json:"targetRef,omitempty"`

	// Selector identifies the Agent to index using label matching.
	// Deprecated: Use TargetRef instead. Selector is kept for backward compatibility.
	// If both TargetRef and Selector are specified, TargetRef takes precedence.
	// +optional
	Selector *AgentSelector `json:"selector,omitempty"`

	// IdentityBinding specifies SPIFFE identity binding configuration
	// +optional
	IdentityBinding *IdentityBinding `json:"identityBinding,omitempty"`
}

// SpiffeID represents a SPIFFE identity in the format spiffe://<trust-domain>/<path>
// +kubebuilder:validation:Pattern=`^spiffe://[a-zA-Z0-9][a-zA-Z0-9\-\.]*[a-zA-Z0-9](/[a-zA-Z0-9\-\._~%!$&'()*+,;=:@]+)*$`
type SpiffeID string

// IdentityBinding configures workload identity binding for an AgentCard
type IdentityBinding struct {
	// TrustDomain overrides the controller's default trust domain.
	// Must be a valid DNS-like string without slashes.
	// +optional
	// +kubebuilder:validation:Pattern=`^[a-zA-Z0-9]([a-zA-Z0-9\-\.]*[a-zA-Z0-9])?$`
	TrustDomain string `json:"trustDomain,omitempty"`

	// ExpectedSpiffeID overrides the auto-derived SPIFFE ID for this workload.
	// Use this when your SPIRE configuration uses a non-standard identity format
	// (e.g., pod labels, container names, or other workload attestor selectors).
	// If not specified, the controller derives the ID as:
	//   spiffe://<trust-domain>/ns/<namespace>/sa/<serviceAccount>
	// See: https://github.com/spiffe/spire/blob/main/doc/plugin_agent_workloadattestor_k8s.md
	// +optional
	ExpectedSpiffeID SpiffeID `json:"expectedSpiffeID,omitempty"`

	// AllowedSpiffeIDs is the allowlist of SPIFFE IDs that can bind to this agent.
	// Each ID must be a valid SPIFFE ID in the format spiffe://<trust-domain>/<path>
	// +required
	// +kubebuilder:validation:MinItems=1
	AllowedSpiffeIDs []SpiffeID `json:"allowedSpiffeIDs"`

	// Strict enables strict enforcement mode. When true and binding fails,
	// the Agent controller will scale the deployment to 0.
	// +optional
	// +kubebuilder:default=false
	Strict bool `json:"strict,omitempty"`
}

// TargetRef identifies a workload that backs this agent using duck typing.
// This allows referencing any workload type (Deployment, StatefulSet, Job, etc.)
// without the controller needing explicit knowledge of each type.
type TargetRef struct {
	// APIVersion is the API version of the target resource (e.g., "apps/v1")
	// +kubebuilder:validation:MinLength=1
	APIVersion string `json:"apiVersion"`

	// Kind is the kind of the target resource (e.g., "Deployment", "StatefulSet")
	// +kubebuilder:validation:MinLength=1
	Kind string `json:"kind"`

	// Name is the name of the target resource
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
}

// AgentSelector identifies which Agent resource to index using label matching.
// Deprecated: Use TargetRef instead for explicit workload references.
type AgentSelector struct {
	// MatchLabels is a map of {key,value} pairs to match against Agent labels
	// +required
	MatchLabels map[string]string `json:"matchLabels"`
}

// AgentCardStatus defines the observed state of AgentCard.
type AgentCardStatus struct {
	// Card contains the cached agent card data
	// +optional
	Card *AgentCardData `json:"card,omitempty"`

	// Conditions represent the current state of the indexing process
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// LastSyncTime is when the agent card was last successfully fetched
	// +optional
	LastSyncTime *metav1.Time `json:"lastSyncTime,omitempty"`

	// Protocol is the detected agent protocol (e.g., "a2a")
	// +optional
	Protocol string `json:"protocol,omitempty"`

	// TargetRef contains the resolved reference to the backing workload.
	// This is populated after the controller successfully locates the workload.
	// +optional
	TargetRef *TargetRef `json:"targetRef,omitempty"`

	// ValidSignature indicates if the agent card signature was validated
	// +optional
	ValidSignature *bool `json:"validSignature,omitempty"`

	// SignatureVerificationDetails contains details about the last signature verification
	// +optional
	SignatureVerificationDetails string `json:"signatureVerificationDetails,omitempty"`

	// SignatureKeyID is the key ID used for verification (from JWS protected header kid)
	// +optional
	SignatureKeyID string `json:"signatureKeyId,omitempty"`

	// SignatureSpiffeID is the SPIFFE ID extracted from the JWS protected header.
	// This enables cross-referencing the signer's identity with the identity binding evaluation.
	// +optional
	SignatureSpiffeID string `json:"signatureSpiffeId,omitempty"`

	// SignatureIdentityMatch indicates if both signature AND identity binding pass.
	// true only when ValidSignature is true AND BindingStatus.Bound is true.
	// +optional
	SignatureIdentityMatch *bool `json:"signatureIdentityMatch,omitempty"`

	// CardId is the SHA256 hash of the JCS-canonicalized card content (optional drift detection)
	// +optional
	CardId string `json:"cardId,omitempty"`

	// ExpectedSpiffeID is the derived SPIFFE ID based on Kubernetes metadata
	// +optional
	ExpectedSpiffeID string `json:"expectedSpiffeID,omitempty"`

	// BindingStatus contains the result of identity binding evaluation
	// +optional
	BindingStatus *BindingStatus `json:"bindingStatus,omitempty"`
}

// BindingStatus represents the result of identity binding evaluation
type BindingStatus struct {
	// Bound indicates whether the expected SPIFFE ID is in the allowlist
	Bound bool `json:"bound"`

	// Reason is a machine-readable reason for the binding status
	// +optional
	Reason string `json:"reason,omitempty"`

	// Message is a human-readable description of the binding status
	// +optional
	Message string `json:"message,omitempty"`

	// LastEvaluationTime is when the binding was last evaluated
	// +optional
	LastEvaluationTime *metav1.Time `json:"lastEvaluationTime,omitempty"`
}

// AgentCardData represents the A2A agent card structure
// Based on the A2A specification
type AgentCardData struct {
	// Name is the human-readable name of the agent
	// +optional
	Name string `json:"name,omitempty"`

	// Description provides information about what the agent does
	// +optional
	Description string `json:"description,omitempty"`

	// Version is the agent's version string
	// +optional
	Version string `json:"version,omitempty"`

	// URL is the endpoint where the A2A service can be reached
	// +optional
	URL string `json:"url,omitempty"`

	// Capabilities specifies supported A2A features
	// +optional
	Capabilities *AgentCapabilities `json:"capabilities,omitempty"`

	// DefaultInputModes are the default media types the agent accepts
	// +optional
	DefaultInputModes []string `json:"defaultInputModes,omitempty"`

	// DefaultOutputModes are the default media types the agent produces
	// +optional
	DefaultOutputModes []string `json:"defaultOutputModes,omitempty"`

	// Skills is a list of skills/capabilities this agent offers
	// +optional
	Skills []AgentSkill `json:"skills,omitempty"`

	// SupportsAuthenticatedExtendedCard indicates if the agent has an extended card
	// +optional
	SupportsAuthenticatedExtendedCard *bool `json:"supportsAuthenticatedExtendedCard,omitempty"`

	// Signatures contains JWS signatures per A2A spec section 8.4.2.
	// Each element uses JWS JSON Serialization with protected header containing
	// the algorithm (alg), key ID (kid), and optional SPIFFE ID (spiffe_id).
	// +optional
	Signatures []AgentCardSignature `json:"signatures,omitempty"`
}

// AgentCardSignature represents a JWS signature on an AgentCard.
// Follows the A2A specification section 8.4.2 — JWS JSON Serialization.
type AgentCardSignature struct {
	// Protected is the base64url-encoded JWS protected header.
	// Decoded, it contains {"alg":"RS256","kid":"key-1","spiffe_id":"spiffe://..."}.
	// +required
	Protected string `json:"protected"`

	// Signature is the base64url-encoded JWS signature value.
	// The signing input is: BASE64URL(protected) || '.' || BASE64URL(canonical_payload)
	// +required
	Signature string `json:"signature"`

	// Header contains optional unprotected JWS header parameters.
	// +optional
	Header *SignatureHeader `json:"header,omitempty"`
}

// SignatureHeader contains unprotected JWS header parameters.
type SignatureHeader struct {
	// Timestamp is when the signature was created (ISO 8601 string)
	// +optional
	Timestamp string `json:"timestamp,omitempty"`
}

// AgentCapabilities defines A2A feature support
type AgentCapabilities struct {
	// Streaming indicates if the agent supports streaming responses
	// +optional
	Streaming *bool `json:"streaming,omitempty"`

	// PushNotifications indicates if the agent supports push notifications
	// +optional
	PushNotifications *bool `json:"pushNotifications,omitempty"`
}

// AgentSkill represents a skill offered by the agent
type AgentSkill struct {
	// Name is the identifier for this skill
	// +optional
	Name string `json:"name,omitempty"`

	// Description explains what this skill does
	// +optional
	Description string `json:"description,omitempty"`

	// InputModes are the media types this skill accepts
	// +optional
	InputModes []string `json:"inputModes,omitempty"`

	// OutputModes are the media types this skill produces
	// +optional
	OutputModes []string `json:"outputModes,omitempty"`

	// Parameters defines the parameters this skill accepts
	// +optional
	Parameters []SkillParameter `json:"parameters,omitempty"`
}

// SkillParameter defines a parameter that a skill accepts
type SkillParameter struct {
	// Name is the parameter name
	// +optional
	Name string `json:"name,omitempty"`

	// Type is the parameter type (e.g., "string", "number", "boolean", "object", "array")
	// +optional
	Type string `json:"type,omitempty"`

	// Description explains what this parameter is for
	// +optional
	Description string `json:"description,omitempty"`

	// Required indicates if this parameter must be provided
	// +optional
	Required *bool `json:"required,omitempty"`

	// Default is the default value for this parameter
	// +optional
	Default string `json:"default,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=agentcards;cards
// +kubebuilder:printcolumn:name="Protocol",type="string",JSONPath=".status.protocol",description="Agent Protocol"
// +kubebuilder:printcolumn:name="Kind",type="string",JSONPath=".status.targetRef.kind",description="Workload Kind"
// +kubebuilder:printcolumn:name="Target",type="string",JSONPath=".status.targetRef.name",description="Target Workload"
// +kubebuilder:printcolumn:name="Agent",type="string",JSONPath=".status.card.name",description="Agent Name"
// +kubebuilder:printcolumn:name="Verified",type="boolean",JSONPath=".status.validSignature",description="Signature Verified"
// +kubebuilder:printcolumn:name="Bound",type="boolean",JSONPath=".status.bindingStatus.bound",description="Identity Bound"
// +kubebuilder:printcolumn:name="Synced",type="string",JSONPath=".status.conditions[?(@.type=='Synced')].status",description="Sync Status"
// +kubebuilder:printcolumn:name="LastSync",type="date",JSONPath=".status.lastSyncTime",description="Last Sync Time"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// AgentCard is the Schema for the agentcards API.
type AgentCard struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   AgentCardSpec   `json:"spec,omitempty"`
	Status AgentCardStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// AgentCardList contains a list of AgentCard.
type AgentCardList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []AgentCard `json:"items"`
}

func init() {
	SchemeBuilder.Register(&AgentCard{}, &AgentCardList{})
}
