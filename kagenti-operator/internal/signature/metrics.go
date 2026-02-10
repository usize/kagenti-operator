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
	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

var (
	// SignatureVerificationTotal tracks the total number of signature verifications
	SignatureVerificationTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "a2a_signature_verification_total",
			Help: "Total number of A2A signature verifications",
		},
		[]string{"provider", "result", "audit_mode"},
	)

	// SignatureVerificationDuration tracks the duration of signature verifications
	SignatureVerificationDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "a2a_signature_verification_duration_seconds",
			Help:    "Duration of A2A signature verifications in seconds",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"provider"},
	)

	// SignatureVerificationErrors tracks signature verification errors
	SignatureVerificationErrors = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "a2a_signature_verification_errors_total",
			Help: "Total number of A2A signature verification errors",
		},
		[]string{"provider", "error_type"},
	)
)

func init() {
	// Register custom metrics with the global Prometheus registry
	metrics.Registry.MustRegister(
		SignatureVerificationTotal,
		SignatureVerificationDuration,
		SignatureVerificationErrors,
	)
}

// RecordVerification records a signature verification result
func RecordVerification(provider string, verified bool, auditMode bool) {
	result := "failed"
	if verified {
		result = "success"
	}
	audit := "false"
	if auditMode {
		audit = "true"
	}
	SignatureVerificationTotal.WithLabelValues(provider, result, audit).Inc()
}

// RecordError records a signature verification error
func RecordError(provider string, errorType string) {
	SignatureVerificationErrors.WithLabelValues(provider, errorType).Inc()
}

