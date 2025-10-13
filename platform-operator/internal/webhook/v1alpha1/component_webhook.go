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
	"context"
	"encoding/json"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	kagentioperatordevv1alpha1 "github.com/kagenti/operator/platform/api/v1alpha1"
)

// nolint:unused
// log is for logging in this package.
var componentlog = logf.Log.WithName("component-resource")

const (
	// ConfigMap naming convention for pipeline templates
	PipelineTemplateConfigMapPrefix = "pipeline-template"

	// Default values
	DefaultPipelineMode = "dev"
)

// +kubebuilder:webhook:path=/mutate-kagenti-operator-dev-v1alpha1-component,mutating=true,failurePolicy=fail,sideEffects=None,groups=kagenti.operator.dev,resources=components,verbs=create;update,versions=v1alpha1,name=mcomponent-v1alpha1.kb.io,admissionReviewVersions=v1

type ComponentCustomDefaulter struct {
	Client client.Client
}

func SetupComponentWebhookWithManager(mgr ctrl.Manager) error {
	defaulter := &ComponentCustomDefaulter{
		Client: mgr.GetClient(),
	}
	return ctrl.NewWebhookManagedBy(mgr).For(&kagentioperatordevv1alpha1.Component{}).
		WithValidator(&ComponentCustomValidator{}).
		WithDefaulter(defaulter).
		Complete()
}

var _ webhook.CustomDefaulter = &ComponentCustomDefaulter{}

// Mutating webhook entry point
func (d *ComponentCustomDefaulter) Default(ctx context.Context, obj runtime.Object) error {
	component, ok := obj.(*kagentioperatordevv1alpha1.Component)

	if !ok {
		return fmt.Errorf("expected an Component object but got %T", obj)
	}
	componentlog.Info("Mutating webbhook for Component", "name", component.GetName())

	// Set suspend to true by default for new Component resources.
	if component.Spec.Suspend == nil {
		suspend := true
		component.Spec.Suspend = &suspend
		componentlog.Info("setting suspend to true", "name", component.Name, "namespace", component.Namespace)
	}
	// Inject Tekton pipeline spec from a deployed template matching component mode (dev, preprod, or prod)
	if d.injectPipelineTemplateSteps(component) {
		err := d.processPipelineConfig(ctx, component)
		if err != nil {
			return fmt.Errorf("failed to process pipeline config: %w", err)
		}

	}
	return nil
}

func (d *ComponentCustomDefaulter) injectPipelineTemplateSteps(component *kagentioperatordevv1alpha1.Component) bool {
	// Only process for components that have build configuration
	if component.Spec.Agent != nil && component.Spec.Agent.Build != nil {
		return component.Spec.Agent.Build.Pipeline.Steps == nil
	}
	if component.Spec.Tool != nil && component.Spec.Tool.Build != nil {
		return component.Spec.Tool.Build.Pipeline.Steps == nil
	}
	return false
}

func (d *ComponentCustomDefaulter) processPipelineConfig(ctx context.Context, component *kagentioperatordevv1alpha1.Component) error {
	var buildSpec *kagentioperatordevv1alpha1.BuildSpec
	componentlog.Info("Mutating webbhook - injecting Tekton pipeline", "name", component.GetName())

	// Get the build spec to work with
	if component.Spec.Agent != nil && component.Spec.Agent.Build != nil {
		buildSpec = component.Spec.Agent.Build
	} else if component.Spec.Tool != nil && component.Spec.Tool.Build != nil {
		buildSpec = component.Spec.Tool.Build
	} else {
		return fmt.Errorf("no build spec found")
	}

	// Skip if custom pipeline is provided
	if buildSpec.Pipeline.Steps != nil {
		componentlog.Info("Mutating webbhook - using user defined Tekton pipeline", "name", component.GetName())
		return nil
	}

	// Get pipeline mode (default to "dev" if not specified)
	mode := buildSpec.Mode
	if mode == "" {
		mode = DefaultPipelineMode
		buildSpec.Mode = mode
	}

	// Load pipeline template from ConfigMap
	template, err := d.getPipelineTemplate(ctx, mode, "kagenti-system")
	if err != nil {
		return fmt.Errorf("failed to get pipeline template for mode %s: %w", mode, err)
	}

	// Validate required parameters
	err = d.validateRequiredParameters(template, buildSpec)
	if err != nil {
		return fmt.Errorf("required parameter validation failed: %w", err)
	}

	// Create the final pipeline by merging template with user parameters
	pipelineSpec, err := d.mergePipelineTemplate(template, buildSpec)
	if err != nil {
		return fmt.Errorf("failed to merge pipeline template: %w", err)
	}

	// Replace the pipeline config with the final merged pipeline
	buildSpec.Pipeline.Steps = pipelineSpec.Steps

	return nil
}

// getPipelineTemplate retrieves pipeline template from ConfigMap
func (d *ComponentCustomDefaulter) getPipelineTemplate(ctx context.Context, mode, namespace string) (*kagentioperatordevv1alpha1.PipelineTemplate, error) {
	configMapName := fmt.Sprintf("%s-%s", PipelineTemplateConfigMapPrefix, mode)
	componentlog.Info("Mutating webbhook - using Tekton pipeline from configMap", "configMap Name", configMapName, "namespace", namespace)

	configMap := &corev1.ConfigMap{}
	err := d.Client.Get(ctx, types.NamespacedName{
		Name:      configMapName,
		Namespace: namespace,
	}, configMap)
	if err != nil {
		return nil, fmt.Errorf("failed to get ConfigMap %s/%s: %w", namespace, configMapName, err)
	}

	// Parse pipeline template from ConfigMap data
	templateData, exists := configMap.Data["template.json"]
	if !exists {
		return nil, fmt.Errorf("template.json not found in ConfigMap %s", configMapName)
	}
	componentlog.Info("Mutating webbhook - found Tekton pipeline in configMap", "configMap Name", configMapName)

	var template kagentioperatordevv1alpha1.PipelineTemplate

	err = json.Unmarshal([]byte(templateData), &template)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal pipeline template from ConfigMap: %w", err)
	}
	componentlog.Info("Mutating webbhook - unmarshalled Tekton pipeline from configMap", "configMap Name", configMapName)

	return &template, nil
}

func (d *ComponentCustomDefaulter) validateRequiredParameters(template *kagentioperatordevv1alpha1.PipelineTemplate, buildSpec *kagentioperatordevv1alpha1.BuildSpec) error {
	userParams := make(map[string]string)
	componentlog.Info("Mutating webbhook - validateRequiredParameters()")

	// Build map of user-provided parameters
	for _, param := range buildSpec.Pipeline.Parameters {
		userParams[param.Name] = param.Value
	}

	// Check global required parameters
	for _, requiredParam := range template.RequiredParameters {
		if _, exists := userParams[requiredParam]; !exists {
			return fmt.Errorf("required parameter '%s' not provided", requiredParam)
		}
	}

	// Check step-specific required parameters
	for _, step := range template.Steps {
		for _, requiredParam := range step.RequiredParameters {
			if _, exists := userParams[requiredParam]; !exists {
				return fmt.Errorf("required parameter '%s' not provided for step '%s'", requiredParam, step.Name)
			}
		}
	}

	return nil
}

// mergePipelineTemplate merges template with user parameters to create final pipeline
func (d *ComponentCustomDefaulter) mergePipelineTemplate(template *kagentioperatordevv1alpha1.PipelineTemplate, buildSpec *kagentioperatordevv1alpha1.BuildSpec) (*kagentioperatordevv1alpha1.PipelineSpec, error) {
	componentlog.Info("Mutating webbhook - mergePipelineTemplate()")
	// Create parameter map from user input
	userParams := make(map[string]string)
	for _, param := range buildSpec.Pipeline.Parameters {
		userParams[param.Name] = param.Value
	}

	var pipelineSteps []kagentioperatordevv1alpha1.PipelineStepSpec

	// Process each step in the template
	for _, stepTemplate := range template.Steps {
		// Skip disabled steps
		if stepTemplate.Enabled != nil && !*stepTemplate.Enabled {
			continue
		}

		// Create final step
		pipelineStep := kagentioperatordevv1alpha1.PipelineStepSpec{
			Name:      stepTemplate.Name,
			ConfigMap: stepTemplate.ConfigMap,
			Enabled:   stepTemplate.Enabled,
		}
		pipelineSteps = append(pipelineSteps, pipelineStep)
	}

	// Create the pipeline
	pipeline := &kagentioperatordevv1alpha1.PipelineSpec{
		Namespace: template.Namespace,
		Steps:     pipelineSteps,
	}

	// Add global parameters if any
	for _, globalParam := range template.GlobalParameters {
		resolvedValue := d.resolveTemplateValue(globalParam.Value, userParams)
		pipeline.Parameters = append(pipeline.Parameters, kagentioperatordevv1alpha1.ParameterSpec{
			Name:  globalParam.Name,
			Value: resolvedValue,
		})
	}

	return pipeline, nil
}

// resolveTemplateValue resolves template variables in parameter values
func (d *ComponentCustomDefaulter) resolveTemplateValue(value string, vars map[string]string) string {
	resolved := value

	// Simple template variable substitution: {{.VarName}}
	for varName, varValue := range vars {
		placeholder := fmt.Sprintf("{{.%s}}", varName)
		resolved = strings.ReplaceAll(resolved, placeholder, varValue)
	}

	return resolved
}

// TODO(user): change verbs to "verbs=create;update;delete" if you want to enable deletion validation.
// NOTE: The 'path' attribute must follow a specific pattern and should not be modified directly here.
// Modifying the path for an invalid path can cause API server errors; failing to locate the webhook.
// +kubebuilder:webhook:path=/validate-kagenti-operator-dev-v1alpha1-component,mutating=false,failurePolicy=fail,sideEffects=None,groups=kagenti.operator.dev,resources=components,verbs=create;update,versions=v1alpha1,name=vcomponent-v1alpha1.kb.io,admissionReviewVersions=v1

// ComponentCustomValidator struct is responsible for validating the Component resource
// when it is created, updated, or deleted.
//
// NOTE: The +kubebuilder:object:generate=false marker prevents controller-gen from generating DeepCopy methods,
// as this struct is used only for temporary operations and does not need to be deeply copied.
type ComponentCustomValidator struct {
	// TODO(user): Add more fields as needed for validation
}

var _ webhook.CustomValidator = &ComponentCustomValidator{}

// ValidateCreate implements webhook.CustomValidator so a webhook will be registered for the type Component.
func (v *ComponentCustomValidator) ValidateCreate(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	component, ok := obj.(*kagentioperatordevv1alpha1.Component)
	if !ok {
		return nil, fmt.Errorf("expected a Component object but got %T", obj)
	}
	componentlog.Info("Validation for Component upon creation", "name", component.GetName())

	errs := v.validateComponentSpec(component)
	if errs != nil {
		return nil, fmt.Errorf("Object validation failed: %s", strings.Join(errs, "; "))
	}
	return nil, nil
}

// ValidateUpdate implements webhook.CustomValidator so a webhook will be registered for the type Component.
func (v *ComponentCustomValidator) ValidateUpdate(ctx context.Context, oldObj, newObj runtime.Object) (admission.Warnings, error) {
	component, ok := newObj.(*kagentioperatordevv1alpha1.Component)
	if !ok {
		return nil, fmt.Errorf("expected a Component object for the newObj but got %T", newObj)
	}
	componentlog.Info("Validation for Component upon update", "name", component.GetName())

	errs := v.validateComponentSpec(component)
	if errs != nil {
		return nil, fmt.Errorf("Object validation failed: %s", strings.Join(errs, "; "))
	}
	return nil, nil
}

// ValidateDelete implements webhook.CustomValidator so a webhook will be registered for the type Component.
func (v *ComponentCustomValidator) ValidateDelete(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	component, ok := obj.(*kagentioperatordevv1alpha1.Component)
	if !ok {
		return nil, fmt.Errorf("expected a Component object but got %T", obj)
	}
	componentlog.Info("Validation for Component upon deletion", "name", component.GetName())

	return nil, nil
}

// validateComponentSpec validates the ComponentSpec for mutually exclusive fields
func (v *ComponentCustomValidator) validateComponentSpec(component *kagentioperatordevv1alpha1.Component) []string {
	var errors []string

	componentlog.Info("Validation of Component Spec", "name", component.GetName())

	// Validate component types - only one should be specified
	componentCount := 0
	if component.Spec.Agent != nil {
		componentCount++
	}
	if component.Spec.Tool != nil {
		componentCount++
	}
	if component.Spec.Infra != nil {
		componentCount++
	}

	componentlog.Info("Validation of Component Spec", "name", component.GetName(), "number of component types specified", componentCount)
	if componentCount == 0 {
		errors = append(errors, "exactly one component type must be specified (agent, tool, or infra)")
	} else if componentCount > 1 {
		errors = append(errors, "only one component type can be specified (agent, tool, or infra are mutually exclusive)")
	}

	// Validate deployer - only one deployment method should be specified
	deployerErrors := v.validateDeployerSpec(&component.Spec.Deployer)
	errors = append(errors, deployerErrors...)

	// If this is a kubernetes deployment, ensure that it only specifies one kubernetes deployment method.
	if component.Spec.Deployer.Kubernetes != nil {
		deployerErrors = v.validateKubernetesSpec(&component.Spec.Deployer)
		errors = append(errors, deployerErrors...)
	}
	return errors
}

// validateDeployerSpec validates the DeployerSpec for mutually exclusive deployment methods
func (v *ComponentCustomValidator) validateDeployerSpec(deployer *kagentioperatordevv1alpha1.DeployerSpec) []string {
	var errors []string

	deployerCount := 0
	if deployer.Helm != nil {
		deployerCount++
	}
	if deployer.Kubernetes != nil {
		deployerCount++
	}
	if deployer.Olm != nil {
		deployerCount++
	}

	if deployerCount == 0 {
		errors = append(errors, "exactly one deployer method must be specified (helm, kubernetes, or olm)")
	} else if deployerCount > 1 {
		errors = append(errors, "only one deployer method can be specified (helm, kubernetes, and olm are mutually exclusive)")
	}
	return errors
}

// validateDeployerSpec validates the kubernetes DeployerSpec for mutually exclusive deployment methods
func (v *ComponentCustomValidator) validateKubernetesSpec(deployer *kagentioperatordevv1alpha1.DeployerSpec) []string {
	var errors []string

	// If Kubernetes deployer is not specified, skip validation
	if deployer.Kubernetes == nil {
		return errors
	}

	kubeDeployCount := 0
	if deployer.Kubernetes.ImageSpec != nil {
		kubeDeployCount++
	}
	if deployer.Kubernetes.Manifest != nil {
		kubeDeployCount++
	}
	if deployer.Kubernetes.PodTemplateSpec != nil {
		kubeDeployCount++
	}

	if kubeDeployCount == 0 {
		errors = append(errors, "exactly one kubernetes deployment method must be specified (image, manifest, or podTemplateSpec)")
	} else if kubeDeployCount > 1 {
		errors = append(errors, "only one kubernetes deployer method can be specified ( image, manifest, or podTemplateSpec are mutually exclusive)")
	}
	// If PodTemplateSpec is used, ensure other fields (except Replicas) are not specified
	if deployer.Kubernetes.PodTemplateSpec != nil {
		var conflictingFields []string

		// Check for conflicting resource specifications
		if deployer.Kubernetes.Resources.Limits != nil || deployer.Kubernetes.Resources.Requests != nil {
			conflictingFields = append(conflictingFields, "resources")
		}

		if len(deployer.Kubernetes.ContainerPorts) > 0 {
			conflictingFields = append(conflictingFields, "containerPorts")
		}

		if len(deployer.Kubernetes.ServicePorts) > 0 {
			conflictingFields = append(conflictingFields, "servicePorts")
		}

		if deployer.Kubernetes.ServiceType != "" {
			conflictingFields = append(conflictingFields, "serviceType")
		}

		if len(deployer.Kubernetes.Volumes) > 0 {
			conflictingFields = append(conflictingFields, "volumes")
		}

		if len(deployer.Kubernetes.VolumeMounts) > 0 {
			conflictingFields = append(conflictingFields, "volumeMounts")
		}

		if len(conflictingFields) > 0 {
			errors = append(errors, fmt.Sprintf("when PodTemplateSpec is specified, the following fields should be omitted (define them in PodTemplateSpec instead): %v. Only 'replicas' field is allowed alongside PodTemplateSpec", conflictingFields))
		}
	}

	return errors
}
