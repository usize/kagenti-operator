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

	// Selector identifies the Agent to index
	// +required
	Selector AgentSelector `json:"selector"`
}

// AgentSelector identifies which Agent resource to index
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

	// ValidSignature indicates if the agent card signature was validated (future use)
	// +optional
	ValidSignature *bool `json:"validSignature,omitempty"`
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
// +kubebuilder:printcolumn:name="Agent",type="string",JSONPath=".status.card.name",description="Agent Name"
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
