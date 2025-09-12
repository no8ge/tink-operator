package controller

import (
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	atopv1alpha1 "github.com/no8ge/tink-operator/api/v1alpha1"
)

const (
	sharedVolumeName  = "shared-data"
	mcContainerName   = "mc"
	defaultReportPath = "/data"

	// event reasons
	reasonCreateDeployment = "CreateDeployment"
	reasonCreateJob        = "CreateJob"
)

// EnsureUploadInjectionForPodSpec injects shared-data volume and mc sidecar if storage/report are set
func EnsureUploadInjectionForPodSpec(
	podSpec *corev1.PodSpec,
	artifacts atopv1alpha1.ArtifactsConfig,
	storage atopv1alpha1.StorageConfig,
	withWatch bool,
	handleInitContainers bool,
) {
	enabled := true
	if artifacts.Path == "" || storage.Bucket == "" || storage.MinioEndpoint == "" {
		enabled = false
	}
	if !enabled {
		return
	}

	volName := sharedVolumeName
	// 确保共享卷存在
	hasVol := false
	for i := range podSpec.Volumes {
		if podSpec.Volumes[i].Name == volName {
			hasVol = true
			break
		}
	}
	if !hasVol {
		podSpec.Volumes = append(podSpec.Volumes, corev1.Volume{
			Name:         volName,
			VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
		})
	}

	// 主容器共享卷挂载
	if len(podSpec.Containers) > 0 {
		rMountPath := artifacts.Path
		if rMountPath == "" {
			rMountPath = defaultReportPath
		}
		hasMount := false
		for j := range podSpec.Containers[0].VolumeMounts {
			vm := podSpec.Containers[0].VolumeMounts[j]
			if (vm.Name == volName && vm.MountPath == rMountPath) || vm.MountPath == rMountPath {
				hasMount = true
				break
			}
		}
		if !hasMount {
			podSpec.Containers[0].VolumeMounts =
				append(podSpec.Containers[0].VolumeMounts,
					corev1.VolumeMount{Name: volName, MountPath: rMountPath})
		}
	}

	// initContainers 挂载卷
	if handleInitContainers && len(podSpec.InitContainers) > 0 {
		rMountPath := artifacts.Path
		if rMountPath == "" {
			rMountPath = defaultReportPath
		}
		for i := range podSpec.InitContainers {
			hasMount := false
			for j := range podSpec.InitContainers[i].VolumeMounts {
				vm := podSpec.InitContainers[i].VolumeMounts[j]
				if (vm.Name == volName && vm.MountPath == rMountPath) || vm.MountPath == rMountPath {
					hasMount = true
					break
				}
			}
			if !hasMount {
				podSpec.InitContainers[i].VolumeMounts =
					append(podSpec.InitContainers[i].VolumeMounts,
						corev1.VolumeMount{Name: volName, MountPath: rMountPath})
			}
		}
	}

	// 注入 mc sidecar
	hasMc := false
	for i := range podSpec.Containers {
		if podSpec.Containers[i].Name == mcContainerName {
			hasMc = true
			break
		}
	}
	if !hasMc {
		sourcePath := artifacts.Path
		if sourcePath == "" {
			sourcePath = defaultReportPath
		}

		// 构建 mc 命令
		cmd := fmt.Sprintf(
			"#!/usr/bin/env bash\nset -euo pipefail\nmc alias set atop-dev %q \"$ACCESS_KEY\" \"$SECRET_KEY\"\n",
			storage.MinioEndpoint,
		)
		if !withWatch {
			cmd += "echo 'InitContainer completed, starting upload...'\n"
		}
		cmd += "TIMESTAMP=$(date +%Y%m%d-%H%M%S)\n"
		cmd += "TARGET_PATH=\"atop-dev/$BUCKET/$PREFIX/${TIMESTAMP}-$POD_NAME\"\n"
		cmd += "echo \"Uploading to: $TARGET_PATH\"\n"
		if withWatch {
			cmd += "mc mirror --overwrite --watch $SOURCE_PATH $TARGET_PATH\n"
		} else {
			cmd += "mc mirror --overwrite $SOURCE_PATH $TARGET_PATH\n"
			cmd += "echo 'Upload completed successfully'\n"
		}

		mc := corev1.Container{
			Name:            mcContainerName,
			Image:           "minio/mc:latest",
			ImagePullPolicy: corev1.PullIfNotPresent,
			Command:         []string{"sh", "-c", cmd},
			Env: []corev1.EnvVar{
				{Name: "ACCESS_KEY", Value: storage.AccessKeySecret},
				{Name: "SECRET_KEY", Value: storage.SecretKeySecret},
				{Name: "BUCKET", Value: storage.Bucket},
				{Name: "PREFIX", Value: storage.Prefix},
				{Name: "SOURCE_PATH", Value: sourcePath},
				{
					Name: "POD_NAME",
					ValueFrom: &corev1.EnvVarSource{
						FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.name"},
					},
				},
			},
			VolumeMounts: []corev1.VolumeMount{{Name: volName, MountPath: sourcePath}},
		}
		podSpec.Containers = append(podSpec.Containers, mc)
	}
}

// AggregateRunnerPhase 根据 Job 条件和历史状态计算 RunnerPhase/ReportStatus
func AggregateRunnerPhase(job *batchv1.Job, prev atopv1alpha1.RunnerPhase) (atopv1alpha1.RunnerPhase, string) {
	phase := atopv1alpha1.RunnerPending
	for i := range job.Status.Conditions {
		c := job.Status.Conditions[i]
		if c.Type == batchv1.JobComplete && c.Status == corev1.ConditionTrue {
			phase = atopv1alpha1.RunnerSucceeded
			break
		}
		if c.Type == batchv1.JobFailed && c.Status == corev1.ConditionTrue {
			phase = atopv1alpha1.RunnerFailed
			break
		}
	}
	if phase == atopv1alpha1.RunnerPending {
		if job.Status.Succeeded > 0 {
			phase = atopv1alpha1.RunnerSucceeded
		} else if job.Status.Failed > 0 {
			phase = atopv1alpha1.RunnerFailed
		} else if job.Status.Active > 0 {
			phase = atopv1alpha1.RunnerRunning
		}
	}
	// terminal 状态不可回退
	if prev == atopv1alpha1.RunnerSucceeded || prev == atopv1alpha1.RunnerFailed {
		phase = prev
	}

	report := "Waiting"
	switch phase {
	case atopv1alpha1.RunnerRunning:
		report = "Uploading"
	case atopv1alpha1.RunnerSucceeded:
		report = "Succeeded"
	case atopv1alpha1.RunnerFailed:
		report = "Failed"
	}
	return phase, report
}

// AggregateExecutorPhase 根据 Deployment 条件计算 Phase/ReportStatus
func AggregateExecutorPhase(dep *appsv1.Deployment) (string, string) {
	phase := "Pending"
	report := "Waiting"

	desired := int32(1)
	if dep.Spec.Replicas != nil {
		desired = *dep.Spec.Replicas
	}

	progressingTrue := false
	availableTrue := false
	deadlineExceeded := false
	for i := range dep.Status.Conditions {
		c := dep.Status.Conditions[i]
		if c.Type == appsv1.DeploymentProgressing && c.Status == corev1.ConditionTrue {
			progressingTrue = true
		}
		if c.Type == appsv1.DeploymentAvailable && c.Status == corev1.ConditionTrue {
			availableTrue = true
		}
		if c.Type == appsv1.DeploymentProgressing &&
			c.Status == corev1.ConditionFalse &&
			c.Reason == "ProgressDeadlineExceeded" {
			deadlineExceeded = true
		}
	}

	switch {
	case deadlineExceeded:
		phase = "Degraded"
		report = "Failed"
	case dep.Status.AvailableReplicas == desired && availableTrue:
		phase = "Available"
		report = "Watching"
	case progressingTrue || dep.Status.UpdatedReplicas < desired || dep.Status.AvailableReplicas < desired:
		phase = "Progressing"
		report = "Waiting"
	default:
		phase = "Pending"
		report = "Waiting"
	}

	return phase, report
}

// BuildObjectMeta 提供通用的 ObjectMeta 构造
func BuildObjectMeta(name, namespace string, owner metav1.Object, kind string) metav1.ObjectMeta {
	return metav1.ObjectMeta{
		Name:      name,
		Namespace: namespace,
		Labels:    map[string]string{"app.kubernetes.io/managed-by": "tink-operator"},
		OwnerReferences: []metav1.OwnerReference{
			*metav1.NewControllerRef(owner, atopv1alpha1.GroupVersion.WithKind(kind)),
		},
	}
}
