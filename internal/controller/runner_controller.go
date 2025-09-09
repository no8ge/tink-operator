package controller

import (
	"context"
	"time"

	batchv1 "k8s.io/api/batch/v1"
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

func buildDesiredRunnerJob(runner *atopv1alpha1.Runner) *batchv1.Job {
	// 复制用户定义的 JobSpec
	jobSpec := runner.Spec.JobSpec.DeepCopy()

	// 确保 Pod 模板有必要的字段
	if jobSpec.Template.Spec.Volumes == nil {
		jobSpec.Template.Spec.Volumes = []corev1.Volume{}
	}
	if jobSpec.Template.Spec.InitContainers == nil {
		jobSpec.Template.Spec.InitContainers = []corev1.Container{}
	}

	// 将用户定义的容器移动到 initContainers
	if len(jobSpec.Template.Spec.Containers) > 0 {
		// 将所有用户容器移动到 initContainers
		for _, container := range jobSpec.Template.Spec.Containers {
			// 确保 initContainer 有正确的重启策略相关设置
			initContainer := container.DeepCopy()
			jobSpec.Template.Spec.InitContainers = append(jobSpec.Template.Spec.InitContainers, *initContainer)
		}
		// 清空原来的 containers，稍后会添加 mc 容器
		jobSpec.Template.Spec.Containers = []corev1.Container{}
	}

	// 注入共享卷与 mc；Runner 使用一次性上传（不 watch）且等待 .done
	// 现在 initContainer 会处理主要工作，mc 作为主容器等待并上传
	ensureUploadInjectionForPodSpec(&jobSpec.Template.Spec, runner.Spec.Artifacts, runner.Spec.Storage, false, true)

	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      runner.Name,
			Namespace: runner.Namespace,
			Labels:    map[string]string{"app.kubernetes.io/managed-by": "tink-operator"},
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(runner, atopv1alpha1.GroupVersion.WithKind("Runner")),
			},
		},
		Spec: *jobSpec,
	}
}

// RunnerReconciler reconciles a Runner object
type RunnerReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
}

//+kubebuilder:rbac:groups=autotest.atop.io,resources=runners,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=autotest.atop.io,resources=runners/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=autotest.atop.io,resources=runners/finalizers,verbs=update
//+kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=batch,resources=jobs/status,verbs=get
//+kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;watch

func (r *RunnerReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	runner := &atopv1alpha1.Runner{}
	if err := r.Get(ctx, req.NamespacedName, runner); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	job := &batchv1.Job{}
	if err := r.Get(ctx, client.ObjectKey{Namespace: runner.Namespace, Name: runner.Name}, job); err != nil {
		if errors.IsNotFound(err) {
			// Do not recreate the Job if Runner already reached a terminal phase (compatible with TTL cleanup)
			if runner.Status.Phase == atopv1alpha1.RunnerSucceeded || runner.Status.Phase == atopv1alpha1.RunnerFailed {
				return ctrl.Result{}, nil
			}

			job = buildDesiredRunnerJob(runner)

			if err := r.Create(ctx, job); err != nil {
				logger.Error(err, "Failed to create Job for Runner", "runner", runner.Name)
				return ctrl.Result{}, err
			}
			r.Recorder.Eventf(runner, corev1.EventTypeNormal, reasonCreateJob, "Created Job %s/%s", job.Namespace, job.Name)
			logger.Info("Created Job for Runner", "runner", runner.Name, "job", job.Name)
			return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
		}
		return ctrl.Result{}, err
	}

	// Job 已存在，利用 Kubernetes 原生的 Job 不可变性
	// 如果需要更新 Job 规格，依赖用户删除现有 Job 或 Runner 资源

	// 状态聚合
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

	// Stick terminal phase: once Succeeded/Failed, never regress
	stickyPhase := runner.Status.Phase
	if stickyPhase != atopv1alpha1.RunnerSucceeded && stickyPhase != atopv1alpha1.RunnerFailed {
		stickyPhase = phase
	}

	// 更新状态
	if runner.Status.Phase != stickyPhase {
		logger.Info("Updating Runner status", "runner", runner.Name, "oldPhase", runner.Status.Phase, "newPhase", stickyPhase)
	}

	runner.Status.Phase = stickyPhase
	switch stickyPhase {
	case atopv1alpha1.RunnerRunning:
		// 当 Job 运行中时，根据 mc 容器状态设置 ReportStatus
		report := "Waiting"
		podList := &corev1.PodList{}
		if err := r.List(ctx, podList, client.InNamespace(runner.Namespace), client.MatchingLabels(map[string]string{"job-name": job.Name})); err == nil {
			for i := range podList.Items {
				p := podList.Items[i]
				for j := range p.Status.ContainerStatuses {
					cs := p.Status.ContainerStatuses[j]
					if cs.Name == mcContainerName {
						if cs.State.Running != nil {
							report = "Uploading"
						}
						break
					}
				}
				if report == "Uploading" {
					break
				}
			}
		}
		runner.Status.ReportStatus = report
	case atopv1alpha1.RunnerSucceeded:
		runner.Status.ReportStatus = "Succeeded"
	case atopv1alpha1.RunnerFailed:
		runner.Status.ReportStatus = "Failed"
	default:
		runner.Status.ReportStatus = "Waiting"
	}

	if err := r.Status().Update(ctx, runner); err != nil {
		logger.Error(err, "Failed to update Runner status", "runner", runner.Name)
		return ctrl.Result{RequeueAfter: time.Second}, err
	}

	return ctrl.Result{}, nil
}

func (r *RunnerReconciler) SetupWithManager(mgr ctrl.Manager) error {
	r.Recorder = mgr.GetEventRecorderFor("runner-controller")
	return ctrl.NewControllerManagedBy(mgr).
		For(&atopv1alpha1.Runner{}).
		Owns(&batchv1.Job{}).
		Complete(r)
}
