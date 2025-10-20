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

// Package distribution provides detection of Kubernetes distributions
// (e.g., vanilla Kubernetes, OpenShift) to enable distribution-specific
// behavior in the operator.
package distribution

import (
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
)

var (
	logger = ctrl.Log.WithName("distribution")
)

// Type represents the Kubernetes distribution type
type Type string

const (
	// vanilla/upstream Kubernetes
	Kubernetes Type = "kubernetes"
	// Red Hat OpenShift
	OpenShift Type = "openshift"
)

// Detect attempts to detect the Kubernetes distribution by checking for
// canonical markers of various distributions.
func Detect(config *rest.Config) Type {
	dc, err := discovery.NewDiscoveryClientForConfig(config)
	if err != nil {
		logger.Error(err, "Failed to create discovery client, defaulting to kubernetes")
		return Kubernetes
	}

	// Leverage the existence of the config.openshift.io/v1 API group
	_, err = dc.ServerResourcesForGroupVersion("config.openshift.io/v1")
	if err == nil {
		logger.Info("Detected OpenShift distribution")
		return OpenShift
	}

	logger.Info("Detected Kubernetes distribution")
	return Kubernetes
}
