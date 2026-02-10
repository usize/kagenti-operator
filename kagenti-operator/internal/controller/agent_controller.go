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

package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	agentv1alpha1 "github.com/kagenti/operator/api/v1alpha1"
	"github.com/kagenti/operator/internal/distribution"
	rbac "github.com/kagenti/operator/internal/rbac"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/retry"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// AgentReconciler reconciles a Agent object
type AgentReconciler struct {
	client.Client
	Scheme                   *runtime.Scheme
	EnableClientRegistration bool
	Distribution             distribution.Type
	Recorder                 record.EventRecorder
}

var (
	logger = ctrl.Log.WithName("controller").WithName("Agent")
)

const (
	CLIENT_REGISTRATION_NAME = "kagenti-client-registration"
	SPIFFY_HELPER_NAME       = "spiffe-helper"
	AGENT_FINALIZER          = "agent.kagenti.dev/finalizer"
)

// +kubebuilder:rbac:groups=agent.kagenti.dev,resources=agents,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=agent.kagenti.dev,resources=agents/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=agent.kagenti.dev,resources=agents/finalizers,verbs=update
// +kubebuilder:rbac:groups=agent.kagenti.dev,resources=agentcards,verbs=get;list;watch
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=services,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=core,resources=serviceaccounts,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;watch
// +kubebuilder:rbac:groups=core,resources=pods/log,verbs=get;list;watch
// +kubebuilder:rbac:groups=core,resources=configmaps,verbs=get;list;watch
// +kubebuilder:rbac:groups=core,resources=secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=roles,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=rolebindings,verbs=get;list;watch;create;update;patch;delete

func (r *AgentReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger.Info("Reconciling Agent", "namespacedName", req.NamespacedName)

	agent := &agentv1alpha1.Agent{}
	err := r.Get(ctx, req.NamespacedName, agent)
	if err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !agent.ObjectMeta.DeletionTimestamp.IsZero() {
		return r.handleDeletion(ctx, agent)
	}

	if !controllerutil.ContainsFinalizer(agent, AGENT_FINALIZER) {
		controllerutil.AddFinalizer(agent, AGENT_FINALIZER)
		if err := r.Update(ctx, agent); err != nil {
			logger.Error(err, "Unable to add finalizer to Agent")
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}
	// if image is not set in the spec, try to fetch it from the AgentBuild object
	image := agent.Spec.ImageSource.Image
	if image == nil {
		// Get the image for the main container
		containerImage, err := r.getContainerImage(ctx, agent)
		if err != nil {
			logger.Error(err, "Failed to get container image",
				"agent", agent.Name,
				"hasImage", agent.Spec.ImageSource.Image != nil,
				"hasBuildRef", agent.Spec.ImageSource.BuildRef != nil)
			return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
		}
		if containerImage == "" {
			logger.Error(fmt.Errorf("empty image returned"), "No image available")
			return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
		}
		image = &containerImage
	}

	deploymentResult, err := r.reconcileAgentDeployment(ctx, agent)
	if err != nil {
		return deploymentResult, err
	}

	serviceResult, err := r.reconcileAgentService(ctx, agent)
	if err != nil {
		return serviceResult, err
	}

	// Enforce identity binding (scale to 0 if strict binding fails)
	if bindingResult, err := r.enforceIdentityBinding(ctx, agent); err != nil {
		return bindingResult, err
	}

	return ctrl.Result{}, nil
}

func (r *AgentReconciler) reconcileAgentDeployment(ctx context.Context, agent *agentv1alpha1.Agent) (ctrl.Result, error) {
	deploymentName := agent.Name
	deployment := &appsv1.Deployment{}

	err := r.Get(ctx, types.NamespacedName{Name: deploymentName, Namespace: agent.Namespace}, deployment)
	if err != nil && errors.IsNotFound(err) {
		deployment, err = r.createDeploymentForAgent(ctx, agent)
		if err != nil {
			logger.Error(err, "Unable to create deployment spec for Agent",
				"agent", agent.Name,
				"namespace", agent.Namespace)
			return ctrl.Result{}, err
		}
		logger.Info("Creating Agent Deployment", "deploymentName", deploymentName)
		if agent.Annotations != nil {
			deployment.ObjectMeta.Annotations = agent.Annotations
		}

		if logger.V(1).Enabled() {
			data, err := json.MarshalIndent(deployment, "", "  ")
			if err != nil {
				logger.V(1).Error(err, "Unable to marshal deployment spec to JSON")
			} else {
				logger.V(1).Info("Deployment spec", "spec", string(data))
			}
		}

		if err := controllerutil.SetControllerReference(agent, deployment, r.Scheme); err != nil {
			logger.Error(err, "Unable to set controller reference for Agent Deployment",
				"agent", agent.Name,
				"namespace", agent.Namespace)
			return ctrl.Result{}, err
		}

		if err := r.Create(ctx, deployment); err != nil {
			logger.Error(err, "Unable to create Agent Deployment",
				"agent", agent.Name,
				"namespace", agent.Namespace)
			return ctrl.Result{RequeueAfter: 5 * time.Second}, err
		}
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	} else if err != nil {
		logger.Error(err, "Failed to get Agent Deployment",
			"agent", agent.Name,
			"namespace", agent.Namespace)
		return ctrl.Result{RequeueAfter: 5 * time.Second}, err
	}

	// Update deployment if spec has changed
	desiredDeployment, err := r.createDeploymentForAgent(ctx, agent)
	if err != nil {
		logger.Error(err, "Unable to create desired deployment spec for Agent",
			"agent", agent.Name,
			"namespace", agent.Namespace)
		return ctrl.Result{}, err
	}

	if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		// Fetch the latest version of the deployment before each retry
		if err := r.Get(ctx, client.ObjectKey{Name: deploymentName, Namespace: agent.Namespace}, deployment); err != nil {
			return err
		}

		logger.Info("Updating Agent Deployment", "deploymentName", deploymentName)

		desiredReplicas := desiredDeployment.Spec.Replicas
		currentReplicas := deployment.Spec.Replicas
		if !ptr.Equal(currentReplicas, desiredReplicas) {
			logger.Info("Replicas changed",
				"old", ptrValueOrDefault(currentReplicas, 1),
				"new", ptrValueOrDefault(desiredReplicas, 1))
			deployment.Spec.Replicas = desiredReplicas
		}

		// Build a map of existing named containers and a slice of indices for unnamed ones.
		existingByName := map[string]int{}
		var unnamedExisting []int
		for i := range deployment.Spec.Template.Spec.Containers {
			c := deployment.Spec.Template.Spec.Containers[i]
			if c.Name == "" {
				unnamedExisting = append(unnamedExisting, i)
				logger.Info("Warning: found unnamed container at index, matching by position is fragile",
					"index", i,
					"deployment", deploymentName)
			} else {
				existingByName[c.Name] = i
			}
		}

		// Apply desired changes by container name; append missing containers.
		// Use a separate index for unnamed desired containers to match them deterministically.
		unnamedIdx := 0
		for i := range desiredDeployment.Spec.Template.Spec.Containers {
			desired := desiredDeployment.Spec.Template.Spec.Containers[i]

			if desired.Name != "" {
				if idx, found := existingByName[desired.Name]; found {
					updated := updateContainerEnv(&deployment.Spec.Template.Spec.Containers[idx], &desired)
					if updated {
						logger.Info("Container updated", "containerName", desired.Name)
					}
					continue
				}
				// not found: append desired container to existing list
				deployment.Spec.Template.Spec.Containers = append(deployment.Spec.Template.Spec.Containers, desired)
				logger.Info("Container added", "containerName", desired.Name)
				continue
			}

			// Fallback for unnamed desired containers: match to next unnamed existing container by recorded index
			if unnamedIdx < len(unnamedExisting) {
				idx := unnamedExisting[unnamedIdx]
				updateContainerEnv(&deployment.Spec.Template.Spec.Containers[idx], &desired)
				unnamedIdx++
			} else {
				// no unnamed existing left: append desired (unnamed) container
				deployment.Spec.Template.Spec.Containers = append(deployment.Spec.Template.Spec.Containers, desired)
			}
		}
		deployment.ObjectMeta.Annotations = mergeStringMaps(deployment.ObjectMeta.Annotations, agent.Annotations)
		deployment.ObjectMeta.Labels = mergeStringMaps(deployment.ObjectMeta.Labels, agent.Labels)
		return r.Update(ctx, deployment)
	}); err != nil {
		logger.Error(err, "Failed to update Agent Deployment after retries",
			"agent", agent.Name,
			"namespace", agent.Namespace)
		return ctrl.Result{RequeueAfter: 5 * time.Second}, err
	}

	if err := r.Get(ctx, types.NamespacedName{Name: deploymentName, Namespace: agent.Namespace}, deployment); err != nil {
		logger.Error(err, "Failed to get updated deployment status",
			"deployment", deploymentName,
			"namespace", agent.Namespace)
		return ctrl.Result{RequeueAfter: 5 * time.Second}, err
	}

	logger.Info("Deployment status",
		"name", deploymentName,
		"namespace", agent.Namespace,
		"desiredReplicas", ptrValueOrDefault(deployment.Spec.Replicas, 1),
		"statusReplicas", deployment.Status.Replicas,
		"readyReplicas", deployment.Status.ReadyReplicas,
		"availableReplicas", deployment.Status.AvailableReplicas,
		"updatedReplicas", deployment.Status.UpdatedReplicas,
		"unavailableReplicas", deployment.Status.UnavailableReplicas,
		"conditions", deployment.Status.Conditions)

	desiredReplicas := ptrValueOrDefault(deployment.Spec.Replicas, 1)

	deploymentMessage := fmt.Sprintf(
		"Replicas: %d/%d ready, %d updated, %d available",
		deployment.Status.ReadyReplicas,
		deployment.Status.Replicas,
		deployment.Status.UpdatedReplicas,
		deployment.Status.AvailableReplicas,
	)

	var phase agentv1alpha1.LifecyclePhase
	var conditions []metav1.Condition

	// DeploymentAvailable condition
	conditions = append(conditions, metav1.Condition{
		Type:               "DeploymentAvailable",
		Status:             metav1.ConditionTrue,
		LastTransitionTime: metav1.Now(),
		Reason:             "DeploymentExists",
		Message:            fmt.Sprintf("Deployment %s exists with %d desired replicas", deployment.Name, desiredReplicas),
	})

	// Track pod scheduling and availability
	if deployment.Status.UnavailableReplicas > 0 {
		conditions = append(conditions, metav1.Condition{
			Type:               "PodsScheduled",
			Status:             metav1.ConditionFalse,
			LastTransitionTime: metav1.Now(),
			Reason:             "PodsUnscheduled",
			Message:            fmt.Sprintf("%d of %d pods are unavailable", deployment.Status.UnavailableReplicas, deployment.Status.Replicas),
		})
	} else if deployment.Status.Replicas > 0 {
		conditions = append(conditions, metav1.Condition{
			Type:               "PodsScheduled",
			Status:             metav1.ConditionTrue,
			LastTransitionTime: metav1.Now(),
			Reason:             "AllPodsScheduled",
			Message:            fmt.Sprintf("All %d pods are scheduled and available", deployment.Status.AvailableReplicas),
		})
	}

	// Update Ready condition and Phase based on deployment readiness
	if deployment.Status.ReadyReplicas > 0 && deployment.Status.ReadyReplicas == int32(desiredReplicas) {
		phase = agentv1alpha1.PhaseReady

		conditions = append(conditions, metav1.Condition{
			Type:               "Ready",
			Status:             metav1.ConditionTrue,
			LastTransitionTime: metav1.Now(),
			Reason:             "DeploymentReady",
			Message:            fmt.Sprintf("All %d/%d replicas are ready", deployment.Status.ReadyReplicas, desiredReplicas),
		})
	} else {
		phase = agentv1alpha1.PhaseDeploying

		conditions = append(conditions, metav1.Condition{
			Type:               "Ready",
			Status:             metav1.ConditionFalse,
			LastTransitionTime: metav1.Now(),
			Reason:             "DeploymentNotReady",
			Message:            fmt.Sprintf("Waiting for replicas: %d/%d ready, %d available", deployment.Status.ReadyReplicas, desiredReplicas, deployment.Status.AvailableReplicas),
		})
	}

	if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		latestAgent := &agentv1alpha1.Agent{}
		if err := r.Get(ctx, types.NamespacedName{Name: agent.Name, Namespace: agent.Namespace}, latestAgent); err != nil {
			return err
		}

		if latestAgent.Status.DeploymentStatus == nil {
			latestAgent.Status.DeploymentStatus = &agentv1alpha1.DeploymentStatus{}
		}

		latestAgent.Status.DeploymentStatus.DeploymentMessage = deploymentMessage
		latestAgent.Status.DeploymentStatus.Phase = phase

		for _, condition := range conditions {
			meta.SetStatusCondition(&latestAgent.Status.Conditions, condition)
		}

		return r.Status().Update(ctx, latestAgent)
	}); err != nil {
		logger.Error(err, "Failed to update Agent status after retries",
			"agent", agent.Name,
			"namespace", agent.Namespace)
		return ctrl.Result{RequeueAfter: 5 * time.Second}, err
	}

	// Continue reconciling if not all replicas are ready
	if deployment.Status.ReadyReplicas < int32(desiredReplicas) {
		logger.Info("Requeuing: not all replicas ready",
			"ready", deployment.Status.ReadyReplicas,
			"desired", desiredReplicas)
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}

	logger.Info("Deployment is ready", "replicas", deployment.Status.ReadyReplicas)
	return ctrl.Result{}, nil
}

func ptrValueOrDefault[T any](ptr *T, defaultVal T) T {
	if ptr == nil {
		return defaultVal
	}
	return *ptr
}

// Returns true if any field was updated
func updateContainerEnv(existing, desired *corev1.Container) bool {
	updated := false

	// Update Env
	if !equality.Semantic.DeepEqual(existing.Env, desired.Env) {
		existing.Env = desired.Env
		updated = true
	}
	return updated
}

// mergeStringMaps overlays src onto dst, returning a map suitable for assigning back.
// If both are nil, returns nil.
func mergeStringMaps(dst, src map[string]string) map[string]string {
	if dst == nil && src == nil {
		return nil
	}
	if dst == nil {
		dst = map[string]string{}
	}
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

func (r *AgentReconciler) fetchImageFromAgentBuild(ctx context.Context, agent *agentv1alpha1.Agent, agentBuildRef string) (string, error) {
	logger.Info("Fetching image from AgentBuild", "buildRef", agentBuildRef, "namespace", agent.Namespace)

	agentBuild := &agentv1alpha1.AgentBuild{}
	err := r.Get(ctx, types.NamespacedName{Name: agentBuildRef, Namespace: agent.Namespace}, agentBuild)
	if err != nil {
		if errors.IsNotFound(err) {
			return "", fmt.Errorf("AgentBuild %s not found in namespace %s", agentBuildRef, agent.Namespace)
		}
		return "", fmt.Errorf("failed to get AgentBuild %s: %w", agentBuildRef, err)
	}

	// Check build phase with specific error messages
	switch agentBuild.Status.Phase {
	case agentv1alpha1.BuildPhaseSucceeded:
		// Build succeeded, check for image
		if agentBuild.Status.BuiltImage == "" {
			return "", fmt.Errorf("AgentBuild %s succeeded but BuiltImage is empty", agentBuildRef)
		}
		logger.Info("Using image from AgentBuild",
			"buildRef", agentBuildRef,
			"image", agentBuild.Status.BuiltImage)
		return agentBuild.Status.BuiltImage, nil

	case agentv1alpha1.PhaseBuilding:
		return "", fmt.Errorf("AgentBuild %s is still building (phase: %s)",
			agentBuildRef, agentBuild.Status.Phase)

	case agentv1alpha1.BuildPhaseFailed:
		return "", fmt.Errorf("AgentBuild %s failed: %s",
			agentBuildRef, agentBuild.Status.Message)

	case agentv1alpha1.BuildPhasePending, "":
		return "", fmt.Errorf("AgentBuild %s is pending (not started yet)", agentBuildRef)

	default:
		return "", fmt.Errorf("AgentBuild %s has unknown phase: %s",
			agentBuildRef, agentBuild.Status.Phase)
	}
}

func (r *AgentReconciler) getContainerImage(ctx context.Context, agent *agentv1alpha1.Agent) (string, error) {
	if agent.Spec.ImageSource.BuildRef != nil {
		image, err := r.fetchImageFromAgentBuild(ctx, agent, agent.Spec.ImageSource.BuildRef.Name)
		if err != nil {
			logger.Error(err, "Unable to fetch image from AgentBuild",
				"buildRef", agent.Spec.ImageSource.BuildRef.Name)

			meta.SetStatusCondition(&agent.Status.Conditions, metav1.Condition{
				Type:               "ImageReady",
				Status:             metav1.ConditionFalse,
				LastTransitionTime: metav1.Now(),
				Reason:             "WaitingForBuild",
				Message:            err.Error(),
			})

			// Update status (best effort)
			_ = r.Status().Update(ctx, agent)
			return "", err
		}
		meta.SetStatusCondition(&agent.Status.Conditions, metav1.Condition{
			Type:               "ImageReady",
			Status:             metav1.ConditionTrue,
			LastTransitionTime: metav1.Now(),
			Reason:             "ImageAvailable",
			Message:            fmt.Sprintf("Using image from AgentBuild: %s", image),
		})

		return image, nil

	} else if agent.Spec.ImageSource.Image != nil && *agent.Spec.ImageSource.Image != "" {
		return *agent.Spec.ImageSource.Image, nil
	}

	return "", fmt.Errorf("no image source specified: must provide either image or buildRef")
}
func (r *AgentReconciler) createDeploymentForAgent(ctx context.Context, agent *agentv1alpha1.Agent) (*appsv1.Deployment, error) {
	if len(agent.Spec.PodTemplateSpec.Spec.Containers) == 0 {
		return nil, fmt.Errorf("no containers defined in PodTemplateSpec")
	}
	replicas := int32(1)
	if agent.Spec.Replicas != nil {
		replicas = int32(*agent.Spec.Replicas)
	}
	podTemplateSpec := agent.Spec.PodTemplateSpec.DeepCopy()

	podTemplateSpec.ObjectMeta.ResourceVersion = ""
	podTemplateSpec.ObjectMeta.UID = ""

	rbacConfig := rbac.GetComponentRBACConfig(agent.Namespace, agent.Name, agent.Labels)
	rbacManager := rbac.NewRBACManager(r.Client, r.Scheme)
	if err := rbacManager.CreateRBACObjects(ctx, rbacConfig, agent); err != nil {
		logger.Error(err, "failed to create RBAC objects")
		return nil, fmt.Errorf("failed to create RBAC objects: %w", err)
	}
	labels := map[string]string{
		"app.kubernetes.io/name": agent.Name,
	}
	for k, v := range agent.Labels {
		if _, exists := labels[k]; !exists {
			labels[k] = v
		}
	}

	for i := range podTemplateSpec.Spec.Containers {
		container := &podTemplateSpec.Spec.Containers[i]

		if i == 0 && agent.Spec.ImageSource.Image != nil {
			container.Image = *agent.Spec.ImageSource.Image
		}

		if len(container.Ports) == 0 {
			container.Ports = []corev1.ContainerPort{
				{
					Name:          "http",
					ContainerPort: 8000,
					Protocol:      corev1.ProtocolTCP,
				},
			}
		}
	}
	// Get the image for the main container
	image, err := r.getContainerImage(ctx, agent)
	if err != nil {
		return nil, fmt.Errorf("no valid image found for Agent %s", agent.Name)
	}
	if agent.Spec.ImageSource.BuildRef != nil {
		logger.Info("Using image from AgentBuild", "buildRef", agent.Spec.ImageSource.BuildRef.Name, "image", image)

		mainContainerFound := false
		for inx := range podTemplateSpec.Spec.Containers {
			if podTemplateSpec.Spec.Containers[inx].Name == "agent" {
				podTemplateSpec.Spec.Containers[inx].Image = image
				mainContainerFound = true
				break
			}
		}
		if !mainContainerFound && len(podTemplateSpec.Spec.Containers) > 0 {
			logger.Info("No container named 'agent' found, using first container")
			podTemplateSpec.Spec.Containers[0].Image = image
		}
	} else {
		logger.Info("Using static image for Agent", "image", image)
	}

	r.addVolumesAndMounts(podTemplateSpec)
	// Set the ServiceAccountName for the pod
	if podTemplateSpec.Spec.ServiceAccountName == "" {
		podTemplateSpec.Spec.ServiceAccountName = rbacConfig.ServiceAccountName
	}

	// Set security context for the pod (if not already specified)
	if podTemplateSpec.Spec.SecurityContext == nil {
		podSecCtx := &corev1.PodSecurityContext{
			RunAsNonRoot: ptr.To(true),
			SeccompProfile: &corev1.SeccompProfile{
				Type: corev1.SeccompProfileTypeRuntimeDefault,
			},
		}

		// On OpenShift, omit these to allow SCC admission controller to inject appropriate values
		if r.Distribution != distribution.OpenShift {
			podSecCtx.RunAsUser = ptr.To(int64(1000))
			podSecCtx.FSGroup = ptr.To(int64(1000))
		}

		podTemplateSpec.Spec.SecurityContext = podSecCtx
	}

	// Set security context for each container (only if not already specified)
	for inx := range podTemplateSpec.Spec.Containers {
		if podTemplateSpec.Spec.Containers[inx].SecurityContext == nil {
			podTemplateSpec.Spec.Containers[inx].SecurityContext = &corev1.SecurityContext{
				AllowPrivilegeEscalation: ptr.To(false),
				Privileged:               ptr.To(false),
				ReadOnlyRootFilesystem:   ptr.To(true),
				Capabilities: &corev1.Capabilities{
					Drop: []corev1.Capability{"ALL"},
				},
			}
		}
	}

	if podTemplateSpec.ObjectMeta.Labels == nil {
		podTemplateSpec.ObjectMeta.Labels = make(map[string]string)
	}
	for k, v := range labels {
		podTemplateSpec.ObjectMeta.Labels[k] = v
	}

	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:        agent.Name,
			Namespace:   agent.Namespace,
			Labels:      labels,
			Annotations: agent.Annotations,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			Template: *podTemplateSpec,
		},
	}, nil
}

func (r *AgentReconciler) volumeExists(podTemplateSpec *corev1.PodTemplateSpec, volumeName string) bool {

	for _, vol := range podTemplateSpec.Spec.Volumes {
		if vol.Name == volumeName {
			return true
		}
	}
	return false
}

func (r *AgentReconciler) addVolumesAndMounts(podTemplateSpec *corev1.PodTemplateSpec) {
	if !hasVolumeMounts(&podTemplateSpec.Spec, "cache") {
		if len(podTemplateSpec.Spec.Containers) > 0 {
			podTemplateSpec.Spec.Containers[0].VolumeMounts =
				append(podTemplateSpec.Spec.Containers[0].VolumeMounts, corev1.VolumeMount{
					Name:      "cache",
					MountPath: "/app/.cache",
				})
		}
	}

	if exists := r.volumeExists(podTemplateSpec, "cache"); !exists {
		podTemplateSpec.Spec.Volumes = append(podTemplateSpec.Spec.Volumes, corev1.Volume{
			Name: "cache",
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{},
			},
		})
	}

}

func hasVolumeMounts(podSpec *corev1.PodSpec, volumeMountName string) bool {
	for _, container := range podSpec.Containers {
		for _, vm := range container.VolumeMounts {
			if vm.Name == volumeMountName {
				return true
			}
		}
	}
	return false
}

func (r *AgentReconciler) reconcileAgentService(ctx context.Context, agent *agentv1alpha1.Agent) (ctrl.Result, error) {
	serviceName := agent.Name
	service := &corev1.Service{}

	err := r.Get(ctx, types.NamespacedName{Name: serviceName, Namespace: agent.Namespace}, service)

	if err != nil && errors.IsNotFound(err) {
		service = r.createServiceForAgent(agent)
		logger.Info("Creating Service", "serviceName", serviceName)

		if err := controllerutil.SetControllerReference(agent, service, r.Scheme); err != nil {
			logger.Error(err, "Failed to set controller reference for Service")
			return ctrl.Result{}, err
		}

		if err := r.Create(ctx, service); err != nil {
			logger.Error(err, "Failed to create Service")
			return ctrl.Result{}, err
		}

		return ctrl.Result{}, nil
	} else if err != nil {
		logger.Error(err, "Failed to get Service")
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

func (r *AgentReconciler) createServiceForAgent(agent *agentv1alpha1.Agent) *corev1.Service {
	labels := map[string]string{
		"app.kubernetes.io/name": agent.Name,
	}
	servicePorts := []corev1.ServicePort{}
	if agent.Spec.ServicePorts == nil {
		servicePorts = append(servicePorts, corev1.ServicePort{
			Name:       "http",
			Protocol:   corev1.ProtocolTCP,
			Port:       8000,
			TargetPort: intstr.FromInt(8000),
		})
	} else {
		servicePorts = agent.Spec.ServicePorts
	}

	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:        agent.Name,
			Namespace:   agent.Namespace,
			Labels:      labels,
			Annotations: agent.Annotations,
		},
		Spec: corev1.ServiceSpec{
			Selector: labels,
			Ports:    servicePorts,
		},
	}
}

// enforceIdentityBinding checks AgentCards selecting this Agent and enforces strict binding
func (r *AgentReconciler) enforceIdentityBinding(ctx context.Context, agent *agentv1alpha1.Agent) (ctrl.Result, error) {
	// List AgentCards in the same namespace
	agentCards := &agentv1alpha1.AgentCardList{}
	if err := r.List(ctx, agentCards, client.InNamespace(agent.Namespace)); err != nil {
		logger.Error(err, "Failed to list AgentCards", "agent", agent.Name)
		return ctrl.Result{}, err
	}

	// Find AgentCards that select this Agent
	var selectingCards []*agentv1alpha1.AgentCard
	for i := range agentCards.Items {
		card := &agentCards.Items[i]
		if r.agentCardSelectsAgent(card, agent) {
			selectingCards = append(selectingCards, card)
		}
	}

	// Check if any strict binding has failed
	var failedBindings []string
	for _, card := range selectingCards {
		if card.Spec.IdentityBinding != nil &&
			card.Spec.IdentityBinding.Strict &&
			(card.Status.BindingStatus == nil || !card.Status.BindingStatus.Bound) {
			reason := "unknown"
			if card.Status.BindingStatus != nil {
				reason = card.Status.BindingStatus.Message
			}
			failedBindings = append(failedBindings, fmt.Sprintf("%s: %s", card.Name, reason))
		}
	}

	// Get the deployment
	deployment := &appsv1.Deployment{}
	if err := r.Get(ctx, types.NamespacedName{Name: agent.Name, Namespace: agent.Namespace}, deployment); err != nil {
		if errors.IsNotFound(err) {
			// Deployment not created yet
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	wasDisabled := agent.Status.BindingEnforcement != nil && agent.Status.BindingEnforcement.DisabledByBinding

	if len(failedBindings) > 0 {
		// Strict binding failed - disable the agent
		return r.disableAgentForBinding(ctx, agent, deployment, failedBindings, wasDisabled)
	}

	// All bindings pass (or no strict bindings) - restore if previously disabled
	if wasDisabled {
		return r.restoreAgentFromBinding(ctx, agent, deployment)
	}

	return ctrl.Result{}, nil
}

// disableAgentForBinding scales the deployment to 0 and annotates it
func (r *AgentReconciler) disableAgentForBinding(ctx context.Context, agent *agentv1alpha1.Agent, deployment *appsv1.Deployment, failedBindings []string, wasDisabled bool) (ctrl.Result, error) {
	reason := fmt.Sprintf("Identity binding failed: %v", failedBindings)

	// Only store original replicas if not already disabled
	if !wasDisabled && deployment.Spec.Replicas != nil && *deployment.Spec.Replicas > 0 {
		originalReplicas := *deployment.Spec.Replicas

		// Update agent status with binding enforcement info
		if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			latestAgent := &agentv1alpha1.Agent{}
			if err := r.Get(ctx, types.NamespacedName{Name: agent.Name, Namespace: agent.Namespace}, latestAgent); err != nil {
				return err
			}
			now := metav1.Now()
			latestAgent.Status.BindingEnforcement = &agentv1alpha1.BindingEnforcementStatus{
				DisabledByBinding: true,
				OriginalReplicas:  &originalReplicas,
				DisabledAt:        &now,
				DisabledReason:    reason,
			}
			meta.SetStatusCondition(&latestAgent.Status.Conditions, metav1.Condition{
				Type:               "BindingEnforced",
				Status:             metav1.ConditionTrue,
				LastTransitionTime: now,
				Reason:             "StrictBindingFailed",
				Message:            reason,
			})
			return r.Status().Update(ctx, latestAgent)
		}); err != nil {
			logger.Error(err, "Failed to update agent binding enforcement status")
			return ctrl.Result{}, err
		}
	}

	// Scale deployment to 0 and add annotations
	if deployment.Spec.Replicas == nil || *deployment.Spec.Replicas > 0 {
		if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			latestDeployment := &appsv1.Deployment{}
			if err := r.Get(ctx, types.NamespacedName{Name: deployment.Name, Namespace: deployment.Namespace}, latestDeployment); err != nil {
				return err
			}
			latestDeployment.Spec.Replicas = ptr.To(int32(0))
			if latestDeployment.Annotations == nil {
				latestDeployment.Annotations = make(map[string]string)
			}
			latestDeployment.Annotations[AnnotationDisabledBy] = DisabledByIdentityBinding
			latestDeployment.Annotations[AnnotationDisabledReason] = reason
			return r.Update(ctx, latestDeployment)
		}); err != nil {
			logger.Error(err, "Failed to scale deployment to 0 for binding enforcement")
			return ctrl.Result{}, err
		}

		logger.Info("Disabled agent due to strict binding failure", "agent", agent.Name, "reason", reason)
		if r.Recorder != nil {
			r.Recorder.Event(agent, corev1.EventTypeWarning, "BindingEnforced",
				fmt.Sprintf("Agent disabled due to strict identity binding failure: %s", reason))
		}
	}

	return ctrl.Result{}, nil
}

// restoreAgentFromBinding restores the deployment replicas after binding passes
func (r *AgentReconciler) restoreAgentFromBinding(ctx context.Context, agent *agentv1alpha1.Agent, deployment *appsv1.Deployment) (ctrl.Result, error) {
	// Get original replicas from status
	originalReplicas := int32(1)
	if agent.Status.BindingEnforcement != nil && agent.Status.BindingEnforcement.OriginalReplicas != nil {
		originalReplicas = *agent.Status.BindingEnforcement.OriginalReplicas
	}
	if agent.Spec.Replicas != nil {
		originalReplicas = *agent.Spec.Replicas
	}

	// Restore deployment replicas and remove annotations
	if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		latestDeployment := &appsv1.Deployment{}
		if err := r.Get(ctx, types.NamespacedName{Name: deployment.Name, Namespace: deployment.Namespace}, latestDeployment); err != nil {
			return err
		}
		latestDeployment.Spec.Replicas = &originalReplicas
		delete(latestDeployment.Annotations, AnnotationDisabledBy)
		delete(latestDeployment.Annotations, AnnotationDisabledReason)
		return r.Update(ctx, latestDeployment)
	}); err != nil {
		logger.Error(err, "Failed to restore deployment replicas")
		return ctrl.Result{}, err
	}

	// Clear binding enforcement status
	if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		latestAgent := &agentv1alpha1.Agent{}
		if err := r.Get(ctx, types.NamespacedName{Name: agent.Name, Namespace: agent.Namespace}, latestAgent); err != nil {
			return err
		}
		latestAgent.Status.BindingEnforcement = nil
		meta.SetStatusCondition(&latestAgent.Status.Conditions, metav1.Condition{
			Type:               "BindingEnforced",
			Status:             metav1.ConditionFalse,
			LastTransitionTime: metav1.Now(),
			Reason:             "BindingRestored",
			Message:            "All strict identity bindings now pass",
		})
		return r.Status().Update(ctx, latestAgent)
	}); err != nil {
		logger.Error(err, "Failed to clear binding enforcement status")
		return ctrl.Result{}, err
	}

	logger.Info("Restored agent after binding passed", "agent", agent.Name, "replicas", originalReplicas)
	if r.Recorder != nil {
		r.Recorder.Event(agent, corev1.EventTypeNormal, "BindingRestored",
			fmt.Sprintf("Agent restored with %d replicas after identity binding passed", originalReplicas))
	}

	return ctrl.Result{}, nil
}

// agentCardSelectsAgent checks if an AgentCard's selector matches an Agent.
// Returns true if the card uses a selector and all its matchLabels are present on the agent,
// or if the card uses a targetRef that references this specific Agent by name.
func (r *AgentReconciler) agentCardSelectsAgent(card *agentv1alpha1.AgentCard, agent *agentv1alpha1.Agent) bool {
	// Check targetRef first (preferred)
	if card.Spec.TargetRef != nil {
		return card.Spec.TargetRef.Kind == "Agent" &&
			card.Spec.TargetRef.Name == agent.Name
	}

	// Fall back to selector (legacy) — guard against nil
	if card.Spec.Selector == nil || agent.Labels == nil {
		return false
	}
	for key, value := range card.Spec.Selector.MatchLabels {
		if agent.Labels[key] != value {
			return false
		}
	}
	return true
}

// mapAgentCardToAgent maps AgentCard events to Agent reconcile requests
func (r *AgentReconciler) mapAgentCardToAgent(ctx context.Context, obj client.Object) []reconcile.Request {
	card, ok := obj.(*agentv1alpha1.AgentCard)
	if !ok {
		return nil
	}

	// Only trigger for cards with strict identity binding
	if card.Spec.IdentityBinding == nil || !card.Spec.IdentityBinding.Strict {
		return nil
	}

	// Find matching Agents
	agents := &agentv1alpha1.AgentList{}
	if err := r.List(ctx, agents, client.InNamespace(card.Namespace)); err != nil {
		logger.Error(err, "Failed to list Agents for AgentCard mapping")
		return nil
	}

	var requests []reconcile.Request
	for _, agent := range agents.Items {
		if r.agentCardSelectsAgent(card, &agent) {
			requests = append(requests, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      agent.Name,
					Namespace: agent.Namespace,
				},
			})
		}
	}

	return requests
}

func (r *AgentReconciler) handleDeletion(ctx context.Context, agent *agentv1alpha1.Agent) (ctrl.Result, error) {
	if controllerutil.ContainsFinalizer(agent, AGENT_FINALIZER) {
		deployment := &appsv1.Deployment{}
		deploymentName := agent.Name
		err := r.Get(ctx, types.NamespacedName{Name: deploymentName, Namespace: agent.Namespace}, deployment)
		if err == nil {
			logger.Info("Deleting deployment for Agent", "deploymentName", deploymentName)
			if err := r.Delete(ctx, deployment); err != nil && !errors.IsNotFound(err) {
				logger.Error(err, "Failed to delete deployment for Agent", "deploymentName", deploymentName)
				return ctrl.Result{}, err
			}
		} else if !errors.IsNotFound(err) {
			logger.Error(err, "Failed to get deployment for deletion", "deploymentName", deploymentName)
			return ctrl.Result{}, err
		}

		service := &corev1.Service{}
		serviceName := agent.Name
		err = r.Get(ctx, types.NamespacedName{Name: serviceName, Namespace: agent.Namespace}, service)
		if err == nil {
			logger.Info("Deleting service for Agent", "serviceName", serviceName)
			if err := r.Delete(ctx, service); err != nil && !errors.IsNotFound(err) {
				logger.Error(err, "Failed to delete service for Agent", "serviceName", serviceName)
				return ctrl.Result{}, err
			}
		} else if !errors.IsNotFound(err) {
			logger.Error(err, "Failed to get service for deletion", "serviceName", serviceName)
			return ctrl.Result{}, err
		}
		if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			latestAgent := &agentv1alpha1.Agent{}
			if err := r.Get(ctx, types.NamespacedName{Name: agent.Name, Namespace: agent.Namespace}, latestAgent); err != nil {
				return err
			}

			controllerutil.RemoveFinalizer(latestAgent, AGENT_FINALIZER)
			return r.Update(ctx, latestAgent)
		}); err != nil {
			logger.Error(err, "Failed to remove finalizer from Agent after retries")
			return ctrl.Result{}, err
		}
		logger.Info("Removed finalizer from Agent")
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *AgentReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&agentv1alpha1.Agent{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{}).
		Watches(
			&agentv1alpha1.AgentCard{},
			handler.EnqueueRequestsFromMapFunc(r.mapAgentCardToAgent),
		).
		Named("Agent").
		Complete(r)
}
