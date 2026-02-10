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

package signature

import (
	"context"

	agentv1alpha1 "github.com/kagenti/operator/api/v1alpha1"
)

// NoOpProvider is a provider that always returns verified=true.
// Used when signature verification is disabled.
type NoOpProvider struct{}

// NewNoOpProvider creates a new NoOp provider
func NewNoOpProvider() Provider {
	return &NoOpProvider{}
}

// VerifySignature always returns success
func (p *NoOpProvider) VerifySignature(ctx context.Context, cardData *agentv1alpha1.AgentCardData, signatures []agentv1alpha1.AgentCardSignature) (*VerificationResult, error) {
	return &VerificationResult{
		Verified: true,
		Details:  "Signature verification disabled",
	}, nil
}

// Name returns the provider name
func (p *NoOpProvider) Name() string {
	return "noop"
}
