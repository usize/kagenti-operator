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

package agentcard

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	agentv1alpha1 "github.com/kagenti/operator/api/v1alpha1"
	ctrl "sigs.k8s.io/controller-runtime"
)

var (
	fetcherLogger = ctrl.Log.WithName("agentcard").WithName("fetcher")
)

const (
	// A2A protocol constants
	A2AProtocol         = "a2a"
	A2AAgentCardPath    = "/.well-known/agent.json"
	DefaultFetchTimeout = 10 * time.Second
)

// Fetcher handles fetching agent cards from various protocols
type Fetcher interface {
	Fetch(ctx context.Context, protocol string, serviceURL string) (*agentv1alpha1.AgentCardData, error)
}

// DefaultFetcher implements the Fetcher interface
type DefaultFetcher struct {
	httpClient *http.Client
}

// NewFetcher creates a new agent card fetcher
func NewFetcher() Fetcher {
	return &DefaultFetcher{
		httpClient: &http.Client{
			Timeout: DefaultFetchTimeout,
		},
	}
}

// Fetch retrieves an agent card based on the protocol
func (f *DefaultFetcher) Fetch(ctx context.Context, protocol string, serviceURL string) (*agentv1alpha1.AgentCardData, error) {
	switch protocol {
	case A2AProtocol:
		return f.fetchA2ACard(ctx, serviceURL)
	default:
		return nil, fmt.Errorf("unsupported protocol: %s", protocol)
	}
}

// fetchA2ACard fetches an A2A agent card from the well-known endpoint
func (f *DefaultFetcher) fetchA2ACard(ctx context.Context, serviceURL string) (*agentv1alpha1.AgentCardData, error) {
	// Construct the agent card URL
	agentCardURL := serviceURL + A2AAgentCardPath
	fetcherLogger.Info("Fetching A2A agent card", "url", agentCardURL)

	// Create the HTTP request
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, agentCardURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// Set headers
	req.Header.Set("Accept", "application/json")

	// Execute the request
	resp, err := f.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch agent card: %w", err)
	}
	defer resp.Body.Close()

	// Check response status
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("unexpected status code %d: %s", resp.StatusCode, string(body))
	}

	// Read and parse the response
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	// Parse the agent card
	var agentCardData agentv1alpha1.AgentCardData
	if err := json.Unmarshal(body, &agentCardData); err != nil {
		return nil, fmt.Errorf("failed to parse agent card JSON: %w", err)
	}

	fetcherLogger.Info("Successfully fetched agent card",
		"name", agentCardData.Name,
		"version", agentCardData.Version,
		"url", agentCardData.URL)

	return &agentCardData, nil
}

// GetServiceURL constructs the service URL for an agent
// Following the pattern from agent_controller.go where services are named <agent-name>-svc
func GetServiceURL(agentName, namespace string, port int32) string {
	// Use cluster DNS for service discovery
	// Format: http://<service-name>.<namespace>.svc.cluster.local:<port>
	return fmt.Sprintf("http://%s-svc.%s.svc.cluster.local:%d", agentName, namespace, port)
}
