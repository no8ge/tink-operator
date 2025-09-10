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
	"reflect"

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

func buildDesiredExecutorDeployment(ex *atopv1alpha1.Executor) *appsv1.Deployment {
	// 复制用户定义的 DeploymentSpec
	deploySpec := ex.Spec.DeploymentSpec.DeepCopy()

	// 确保 Pod 模板有必要的字段
	if deploySpec.Template.Spec.Volumes == nil {
		deploySpec.Template.Spec.Volumes = []corev1.Volume{}
	}

	// 确保有 app 标签用于 selector
	if deploySpec.Template.ObjectMeta.Labels == nil {
		deploySpec.Template.ObjectMeta.Labels = map[string]string{}
	}
	deploySpec.Template.ObjectMeta.Labels["app"] = ex.Name

	// 确保有 selector
	if deploySpec.Selector == nil {
		deploySpec.Selector = &metav1.LabelSelector{
			MatchLabels: map[string]string{"app": ex.Name},
		}
	}

	// 注入共享卷与 mc 边车（watch 模式用于长期运行服务）
	// Executor 需要处理 InitContainer
	ensureUploadInjectionForPodSpec(&deploySpec.Template.Spec, ex.Spec.Artifacts, ex.Spec.Storage, true, true)

	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      ex.Name,
			Namespace: ex.Namespace,
			Labels:    map[string]string{"app.kubernetes.io/managed-by": "tink-operator"},
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(ex, atopv1alpha1.GroupVersion.WithKind("Executor")),
			},
		},
		Spec: *deploySpec,
	}
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
	logger := log.FromContext(ctx)

	dep := &appsv1.Deployment{}
	if err := r.Get(ctx, client.ObjectKey{Namespace: ex.Namespace, Name: ex.Name}, dep); err != nil {
		if errors.IsNotFound(err) {
			dep = buildDesiredExecutorDeployment(ex)

			if err := r.Create(ctx, dep); err != nil {
				logger.Error(err, "Failed to create Deployment for Executor", "executor", ex.Name)
				return ctrl.Result{}, err
			}
			r.Recorder.Eventf(ex, corev1.EventTypeNormal, reasonCreateDeployment, "Created Deployment %s/%s", dep.Namespace, dep.Name)
			logger.Info("Created Deployment for Executor", "executor", ex.Name, "deployment", dep.Name)
			return ctrl.Result{}, nil
		}
		logger.Error(err, "Failed to get Deployment", "executor", ex.Name)
		return ctrl.Result{}, err
	}

	// 使用 Kubernetes 原生的 Deployment 滚动更新
	// 通过比较整个 DeploymentSpec 来检测变更
	desiredDep := buildDesiredExecutorDeployment(ex)
	if !reflect.DeepEqual(dep.Spec, desiredDep.Spec) {
		logger.Info("Executor spec changed, updating Deployment", "executor", ex.Name)
		dep.Spec = desiredDep.Spec
		if err := r.Update(ctx, dep); err != nil {
			logger.Error(err, "Failed to update Deployment", "executor", ex.Name)
			return ctrl.Result{}, err
		}
		logger.Info("Updated Deployment for Executor", "executor", ex.Name)
	}

	// 更新状态
	phase := "Pending"
	report := "Waiting"

	desired := int32(1)
	if dep.Spec.Replicas != nil {
		desired = *dep.Spec.Replicas
	}

	// 条件探测
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
		if c.Type == appsv1.DeploymentProgressing && c.Status == corev1.ConditionFalse && c.Reason == "ProgressDeadlineExceeded" {
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

	if ex.Status.Phase != phase || ex.Status.ReportStatus != report {
		logger.Info("Updating Executor status", "executor", ex.Name, "oldPhase", ex.Status.Phase, "newPhase", phase, "report", report)
	}

	ex.Status.Phase = phase
	ex.Status.ReportStatus = report
	if err := r.Status().Update(ctx, ex); err != nil {
		logger.Error(err, "Failed to update Executor status", "executor", ex.Name)
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// ensureUploadInjectionForPodSpec injects shared-data volume and mc sidecar if storage/report are set
func ensureUploadInjectionForPodSpec(podSpec *corev1.PodSpec, artifacts atopv1alpha1.ArtifactsConfig, storage atopv1alpha1.StorageConfig, withWatch bool, handleInitContainers bool) {
	// 启用条件：必须同时具备 report.path 与 storage 关键字段
	enabled := true
	if artifacts.Path == "" || storage.Bucket == "" || storage.MinioEndpoint == "" {
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
		rMountPath := artifacts.Path
		if rMountPath == "" {
			rMountPath = defaultReportPath
		}
		hasMount := false
		for j := range podSpec.Containers[targetIdx].VolumeMounts {
			vm := podSpec.Containers[targetIdx].VolumeMounts[j]
			// 检查是否已经有相同的卷挂载或相同的挂载路径
			if (vm.Name == volName && vm.MountPath == rMountPath) || vm.MountPath == rMountPath {
				hasMount = true
				break
			}
		}
		if !hasMount {
			podSpec.Containers[targetIdx].VolumeMounts = append(podSpec.Containers[targetIdx].VolumeMounts, corev1.VolumeMount{Name: volName, MountPath: rMountPath})
		}
		// Runner 模式下，用户容器已移到 initContainers
		// 不需要为 containers 添加 .done 文件逻辑
	}
	// 同步挂载共享卷到 initContainer，以便其输出与 mc 共享
	if handleInitContainers && len(podSpec.InitContainers) > 0 {
		rMountPath := artifacts.Path
		if rMountPath == "" {
			rMountPath = defaultReportPath
		}

		// 为所有 initContainer 添加共享卷挂载和 .done 文件创建逻辑
		for i := range podSpec.InitContainers {
			// 检查并添加卷挂载
			hasMount := false
			for j := range podSpec.InitContainers[i].VolumeMounts {
				vm := podSpec.InitContainers[i].VolumeMounts[j]
				if (vm.Name == volName && vm.MountPath == rMountPath) || vm.MountPath == rMountPath {
					hasMount = true
					break
				}
			}
			if !hasMount {
				podSpec.InitContainers[i].VolumeMounts = append(podSpec.InitContainers[i].VolumeMounts, corev1.VolumeMount{Name: volName, MountPath: rMountPath})
			}

			// InitContainer 完成后，Kubernetes 会自动启动主容器
			// 不需要额外的 .done 文件信号机制
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
		sourcePath := artifacts.Path
		if sourcePath == "" {
			sourcePath = defaultReportPath
		}
		// 构建 mc 命令，使用转义防止注入
		cmd := fmt.Sprintf("#!/usr/bin/env bash\nset -euo pipefail\nmc alias set atop-dev %q \"$ACCESS_KEY\" \"$SECRET_KEY\"\n", storage.MinioEndpoint)
		if !withWatch {
			// Runner 模式：InitContainer 完成后，Kubernetes 自动启动此容器
			// 直接开始上传，无需等待文件信号
			cmd += "echo 'InitContainer completed, starting upload...'\n"
		}

		// 使用时间戳和 Pod 名称构建唯一的目标路径，避免不同实例覆盖
		// 格式：bucket/prefix/timestamp-podname/
		cmd += "TIMESTAMP=$(date +%Y%m%d-%H%M%S)\n"
		cmd += "TARGET_PATH=\"atop-dev/$BUCKET/$PREFIX/${TIMESTAMP}-$POD_NAME\"\n"
		cmd += "echo \"Uploading to: $TARGET_PATH\"\n"

		if withWatch {
			// Executor 模式：实时监控并上传文件变化
			// 去掉 --remove 避免不同实例互相删除文件
			cmd += "mc mirror --overwrite --watch $SOURCE_PATH $TARGET_PATH\n"
		} else {
			// Runner 模式：一次性上传所有文件
			// 去掉 --remove 避免不同实例互相删除文件
			cmd += "mc mirror --overwrite $SOURCE_PATH $TARGET_PATH\n"
			cmd += "echo 'Upload completed successfully'\n"
		}

		// 使用明文传递账号密码
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
				// 使用 Downward API 获取 Pod 信息
				{
					Name: "POD_NAME",
					ValueFrom: &corev1.EnvVarSource{
						FieldRef: &corev1.ObjectFieldSelector{
							FieldPath: "metadata.name",
						},
					},
				},
			},
			VolumeMounts: []corev1.VolumeMount{{Name: volName, MountPath: sourcePath}},
		}
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
