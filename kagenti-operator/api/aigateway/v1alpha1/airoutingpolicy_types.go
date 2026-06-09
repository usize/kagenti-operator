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

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// AIRoutingPolicySpec defines providers and models for LLM routing.
type AIRoutingPolicySpec struct {
	// TargetRef identifies the Gateway this policy attaches to.
	TargetRef TargetRef `json:"targetRef"`

	// Providers defines the LLM provider endpoints and credentials.
	Providers []ProviderSpec `json:"providers"`

	// Models defines the client-facing models with backend routing.
	Models []ModelSpec `json:"models"`
}

// AIRoutingPolicyStatus defines the observed state of AIRoutingPolicy.
type AIRoutingPolicyStatus struct {
	// Conditions represent the latest available observations of the policy's state.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=airp
// +kubebuilder:printcolumn:name="Gateway",type=string,JSONPath=`.spec.targetRef.name`
// +kubebuilder:printcolumn:name="Providers",type=integer,JSONPath=`.spec.providers`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// AIRoutingPolicy attaches LLM routing configuration to a Gateway.
type AIRoutingPolicy struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   AIRoutingPolicySpec   `json:"spec,omitempty"`
	Status AIRoutingPolicyStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// AIRoutingPolicyList contains a list of AIRoutingPolicy.
type AIRoutingPolicyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []AIRoutingPolicy `json:"items"`
}

func init() {
	SchemeBuilder.Register(&AIRoutingPolicy{}, &AIRoutingPolicyList{})
}
