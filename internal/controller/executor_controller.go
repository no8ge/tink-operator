/*
Copyright 2024.

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
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	atopv1alpha1 "github.com/no8ge/tink-operator/api/v1alpha1"
)

// executorTemplateHashInput captures only fields that affect PodTemplate rendering
type executorTemplateHashInput struct {
	Image            string                        `json:"image"`
	Command          []string                      `json:"command,omitempty"`
	Env              []corev1.EnvVar               `json:"env,omitempty"`
	Resources        corev1.ResourceRequirements   `json:"resources,omitempty"`
	Ports            []corev1.ContainerPort        `json:"ports,omitempty"`
	ImagePullSecrets []corev1.LocalObjectReference `json:"imagePullSecrets,omitempty"`

	ReportPath string `json:"reportPath"`
	WithWatch  bool   `json:"withWatch"`
	Storage    struct {
		Bucket        string `json:"bucket"`
		Prefix        string `json:"prefix"`
		MinioEndpoint string `json:"minioEndpoint"`
		AccessKey     string `json:"accessKey"`
		SecretKey     string `json:"secretKey"`
	} `json:"storage"`
}

func computeSpecHashForExecutor(ex *atopv1alpha1.Executor, withWatch bool) string {
	reportPath := normalizeReportPath(ex.Spec.Artifacts.Path)
	input := executorTemplateHashInput{
		Image:            ex.Spec.Image,
		Command:          ex.Spec.Command,
		Env:              sortedEnv(ex.Spec.Env),
		Resources:        ex.Spec.Resources,
		Ports:            sortedPorts(ex.Spec.Ports),
		ImagePullSecrets: sortedImagePullSecrets(ex.Spec.ImagePullSecrets),
		ReportPath:       reportPath,
		WithWatch:        withWatch,
	}
	input.Storage.Bucket = ex.Spec.Storage.Bucket
	input.Storage.Prefix = ex.Spec.Storage.Prefix
	input.Storage.MinioEndpoint = ex.Spec.Storage.MinioEndpoint
	input.Storage.AccessKey = ex.Spec.Storage.AccessKeySecret
	input.Storage.SecretKey = ex.Spec.Storage.SecretKeySecret

	return computeSHA256Hex(input)
}

// buildDesiredExecutorTemplate renders the full PodTemplate including volume/mounts and sidecar
func buildDesiredExecutorTemplate(ex *atopv1alpha1.Executor) corev1.PodTemplateSpec {
	labels := map[string]string{"app": ex.Name}
	pod := corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{Labels: labels},
		Spec: corev1.PodSpec{
			ImagePullSecrets: ex.Spec.ImagePullSecrets,
			Volumes:          []corev1.Volume{},
			Containers: []corev1.Container{
				{
					Name:            "server",
					Image:           ex.Spec.Image,
					ImagePullPolicy: corev1.PullIfNotPresent,
					Command:         ex.Spec.Command,
					Env:             ex.Spec.Env,
					Resources:       ex.Spec.Resources,
					Ports:           ex.Spec.Ports,
				},
			},
		},
	}
	// inject shared volume, mounts and mc sidecar
	ensureUploadInjectionForPodSpec(&pod.Spec, ex, true)
	return pod
}

// ExecutorReconciler reconciles an Executor object
type ExecutorReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
}

//+kubebuilder:rbac:groups=autotest.atop.io,resources=executors,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=autotest.atop.io,resources=executors/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=autotest.atop.io,resources=executors/finalizers,verbs=update
//+kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=apps,resources=deployments/status,verbs=get
//+kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;watch;create;update;patch;delete

func (r *ExecutorReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	ex := &atopv1alpha1.Executor{}
	if err := r.Get(ctx, req.NamespacedName, ex); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		log.Error(err, "failed to get Executor")
		return ctrl.Result{}, err
	}

	return r.reconcileDeployment(ctx, ex)
}

func (r *ExecutorReconciler) reconcileDeployment(ctx context.Context, ex *atopv1alpha1.Executor) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	dep := &appsv1.Deployment{}
	if err := r.Get(ctx, client.ObjectKey{Namespace: ex.Namespace, Name: ex.Name}, dep); err != nil {
		if errors.IsNotFound(err) {
			replicas := int32(1)
			if ex.Spec.Replicas != nil {
				replicas = *ex.Spec.Replicas
			}
			pod := buildDesiredExecutorTemplate(ex)
			// set spec hash annotation for rolling update detection
			if pod.Annotations == nil {
				pod.Annotations = map[string]string{}
			}
			pod.Annotations[annotationSpecHash] = computeSpecHashForExecutor(ex, true)
			dep = &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:            ex.Name,
					Namespace:       ex.Namespace,
					Labels:          map[string]string{"app.kubernetes.io/managed-by": "tink-operator"},
					OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(ex, atopv1alpha1.GroupVersion.WithKind("Executor"))},
				},
				Spec: appsv1.DeploymentSpec{Replicas: &replicas, Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": ex.Name}}, Template: pod},
			}
			if err := r.Create(ctx, dep); err != nil {
				log.Error(err, "failed to create Deployment")
				return ctrl.Result{}, err
			}
			r.Recorder.Eventf(ex, corev1.EventTypeNormal, reasonCreateDeployment, "created Deployment %s/%s", dep.Namespace, dep.Name)
		} else {
			log.Error(err, "failed to get Deployment")
			return ctrl.Result{}, err
		}
	}

	// If exists, ensure spec hash and template are up to date
	desiredPod := buildDesiredExecutorTemplate(ex)
	if desiredPod.Annotations == nil {
		desiredPod.Annotations = map[string]string{}
	}
	newHash := computeSpecHashForExecutor(ex, true)
	desiredPod.Annotations[annotationSpecHash] = newHash

	oldHash := ""
	if dep.Spec.Template.Annotations != nil {
		oldHash = dep.Spec.Template.Annotations[annotationSpecHash]
	}
	// Update replicas regardless of template changes
	replicas := int32(1)
	if ex.Spec.Replicas != nil {
		replicas = *ex.Spec.Replicas
	}
	if dep.Spec.Replicas == nil || *dep.Spec.Replicas != replicas {
		dep.Spec.Replicas = &replicas
	}
	if oldHash != newHash {
		dep.Spec.Template = desiredPod
		if err := r.Update(ctx, dep); err != nil {
			log.Error(err, "failed to update Deployment template")
			return ctrl.Result{}, err
		}
		r.Recorder.Eventf(ex, corev1.EventTypeNormal, reasonUpdateDeploymentTpl, "updated Deployment template due to spec change: oldHash=%s newHash=%s", oldHash, newHash)
	}

	// 更新状态
	phase := "Pending"
	if dep.Status.ReadyReplicas > 0 {
		phase = "Available"
	}
	ex.Status.Phase = phase
	ex.Status.ReportStatus = ""
	if err := r.Status().Update(ctx, ex); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

// ensureUploadInjectionForPodSpec injects shared-data volume and mc sidecar if storage/report are set
func ensureUploadInjectionForPodSpec(podSpec *corev1.PodSpec, m *atopv1alpha1.Executor, withWatch bool) {
	// 启用条件：必须同时具备 report.path 与 storage 关键字段
	enabled := true
	if m.Spec.Artifacts.Path == "" || m.Spec.Storage.Bucket == "" || m.Spec.Storage.MinioEndpoint == "" {
		enabled = false
	}
	if !enabled {
		return
	}
	volName := sharedVolumeName
	hasVol := false
	for i := range podSpec.Volumes {
		if podSpec.Volumes[i].Name == volName {
			hasVol = true
			break
		}
	}
	if !hasVol {
		podSpec.Volumes = append(podSpec.Volumes, corev1.Volume{Name: volName, VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}})
	}
	// 选择容器并挂载：默认第一个容器
	targetIdx := 0
	if len(podSpec.Containers) > 0 {
		rMountPath := m.Spec.Artifacts.Path
		if rMountPath == "" {
			rMountPath = defaultReportPath
		}
		hasMount := false
		for j := range podSpec.Containers[targetIdx].VolumeMounts {
			vm := podSpec.Containers[targetIdx].VolumeMounts[j]
			if vm.Name == volName || vm.MountPath == rMountPath {
				hasMount = true
				break
			}
		}
		if !hasMount {
			podSpec.Containers[targetIdx].VolumeMounts = append(podSpec.Containers[targetIdx].VolumeMounts, corev1.VolumeMount{Name: volName, MountPath: rMountPath})
		}
	}
	// 同步挂载共享卷到第一个 initContainer，以便其输出与 mc 共享
	if len(podSpec.InitContainers) > 0 {
		rMountPath := m.Spec.Artifacts.Path
		if rMountPath == "" {
			rMountPath = defaultReportPath
		}
		hasMount := false
		for j := range podSpec.InitContainers[0].VolumeMounts {
			vm := podSpec.InitContainers[0].VolumeMounts[j]
			if vm.Name == volName || vm.MountPath == rMountPath {
				hasMount = true
				break
			}
		}
		if !hasMount {
			podSpec.InitContainers[0].VolumeMounts = append(podSpec.InitContainers[0].VolumeMounts, corev1.VolumeMount{Name: volName, MountPath: rMountPath})
		}
	}
	// 注入 mc
	hasMc := false
	for i := range podSpec.Containers {
		if podSpec.Containers[i].Name == mcContainerName {
			hasMc = true
			break
		}
	}
	if !hasMc {
		sourcePath := m.Spec.Artifacts.Path
		if sourcePath == "" {
			sourcePath = defaultReportPath
		}
		cmd := fmt.Sprintf("#!/usr/bin/env bash\nset -euo pipefail\nmc alias set atop-dev \"%s\" \"$ACCESS_KEY\" \"$SECRET_KEY\"\n", m.Spec.Storage.MinioEndpoint)
		if withWatch {
			cmd += fmt.Sprintf("mc mirror --overwrite --watch --remove \"%s\" \"atop-dev/%s/%s\"\n", sourcePath, m.Spec.Storage.Bucket, m.Spec.Storage.Prefix)
		} else {
			cmd += fmt.Sprintf("mc mirror --overwrite --remove \"%s\" \"atop-dev/%s/%s\"\n", sourcePath, m.Spec.Storage.Bucket, m.Spec.Storage.Prefix)
		}
		mc := corev1.Container{Name: mcContainerName, Image: "minio/mc:latest", ImagePullPolicy: corev1.PullIfNotPresent, Command: []string{"sh", "-c", cmd}, Env: []corev1.EnvVar{{Name: "ACCESS_KEY", Value: m.Spec.Storage.AccessKeySecret}, {Name: "SECRET_KEY", Value: m.Spec.Storage.SecretKeySecret}}, VolumeMounts: []corev1.VolumeMount{{Name: volName, MountPath: sourcePath}}}
		podSpec.Containers = append(podSpec.Containers, mc)
	}
}

func (r *ExecutorReconciler) SetupWithManager(mgr ctrl.Manager) error {
	r.Recorder = mgr.GetEventRecorderFor("executor-controller")
	return ctrl.NewControllerManagedBy(mgr).
		For(&atopv1alpha1.Executor{}).
		Owns(&appsv1.Deployment{}).
		Complete(r)
}
