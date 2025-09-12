package controller

import (
	"context"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
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
		for _, container := range jobSpec.Template.Spec.Containers {
			initContainer := container.DeepCopy()
			jobSpec.Template.Spec.InitContainers =
				append(jobSpec.Template.Spec.InitContainers, *initContainer)
		}
		jobSpec.Template.Spec.Containers = []corev1.Container{}
	}

	// 注入共享卷与 mc（Runner 使用一次性上传）
	EnsureUploadInjectionForPodSpec(&jobSpec.Template.Spec, runner.Spec.Artifacts, runner.Spec.Storage, false, true)

	return &batchv1.Job{
		ObjectMeta: BuildObjectMeta(runner.Name, runner.Namespace, runner, "Runner"),
		Spec:       *jobSpec,
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
			// Runner 已经到达终态时，不再重建 Job
			if runner.Status.Phase == atopv1alpha1.RunnerSucceeded || runner.Status.Phase == atopv1alpha1.RunnerFailed {
				return ctrl.Result{}, nil
			}

			job = buildDesiredRunnerJob(runner)
			if err := r.Create(ctx, job); err != nil {
				logger.Error(err, "Failed to create Job for Runner", "runner", runner.Name)
				return ctrl.Result{}, err
			}
			r.Recorder.Eventf(runner, corev1.EventTypeNormal, reasonCreateJob,
				"Created Job %s/%s", job.Namespace, job.Name)
			logger.Info("Created Job for Runner", "runner", runner.Name, "job", job.Name)
			return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
		}
		return ctrl.Result{}, err
	}

	// 计算状态
	newPhase, report := AggregateRunnerPhase(job, runner.Status.Phase)

	if runner.Status.Phase != newPhase || runner.Status.ReportStatus != report {
		logger.Info("Updating Runner status",
			"runner", runner.Name,
			"oldPhase", runner.Status.Phase,
			"newPhase", newPhase,
			"report", report,
		)
		runner.Status.Phase = newPhase
		runner.Status.ReportStatus = report
		if err := r.Status().Update(ctx, runner); err != nil {
			logger.Error(err, "Failed to update Runner status", "runner", runner.Name)
			return ctrl.Result{RequeueAfter: time.Second}, err
		}
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
