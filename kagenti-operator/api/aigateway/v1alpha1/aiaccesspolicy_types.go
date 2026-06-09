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

// AIAccessPolicySpec defines mTLS access control for a Gateway.
type AIAccessPolicySpec struct {
	// TargetRef identifies the Gateway this policy attaches to.
	TargetRef TargetRef `json:"targetRef"`

	// MTLS defines mTLS configuration using SPIFFE trust bundles.
	MTLS MTLSSpec `json:"mtls"`
}

// AIAccessPolicyStatus defines the observed state of AIAccessPolicy.
type AIAccessPolicyStatus struct {
	// Conditions represent the latest available observations of the policy's state.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=aiap
// +kubebuilder:printcolumn:name="Gateway",type=string,JSONPath=`.spec.targetRef.name`
// +kubebuilder:printcolumn:name="TrustDomain",type=string,JSONPath=`.spec.mtls.trustDomain`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// AIAccessPolicy attaches mTLS access control to a Gateway.
type AIAccessPolicy struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   AIAccessPolicySpec   `json:"spec,omitempty"`
	Status AIAccessPolicyStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// AIAccessPolicyList contains a list of AIAccessPolicy.
type AIAccessPolicyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []AIAccessPolicy `json:"items"`
}

func init() {
	SchemeBuilder.Register(&AIAccessPolicy{}, &AIAccessPolicyList{})
}
