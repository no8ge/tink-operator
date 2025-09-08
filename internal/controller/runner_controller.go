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

const (
	annotationSpecHashRunner = "autotest.atop.io/spec-hash"
)

type runnerTemplateHashInput struct {
	Image            string                        `json:"image"`
	Command          []string                      `json:"command,omitempty"`
	Env              []corev1.EnvVar               `json:"env,omitempty"`
	ImagePullSecrets []corev1.LocalObjectReference `json:"imagePullSecrets,omitempty"`
	BackoffLimit     *int32                        `json:"backoffLimit,omitempty"`

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

func computeSpecHashForRunner(r *atopv1alpha1.Runner, withWatch bool) string {
	reportPath := normalizeReportPath(r.Spec.Artifacts.Path)
	input := runnerTemplateHashInput{
		Image:            r.Spec.Image,
		Command:          r.Spec.Command,
		Env:              sortedEnv(r.Spec.Env),
		ImagePullSecrets: sortedImagePullSecrets(r.Spec.ImagePullSecrets),
		BackoffLimit:     r.Spec.BackoffLimit,
		ReportPath:       reportPath,
		WithWatch:        withWatch,
	}
	input.Storage.Bucket = r.Spec.Storage.Bucket
	input.Storage.Prefix = r.Spec.Storage.Prefix
	input.Storage.MinioEndpoint = r.Spec.Storage.MinioEndpoint
	input.Storage.AccessKey = r.Spec.Storage.AccessKeySecret
	input.Storage.SecretKey = r.Spec.Storage.SecretKeySecret

	return computeSHA256Hex(input)
}

func buildDesiredRunnerTemplate(runner *atopv1alpha1.Runner) corev1.PodTemplateSpec {
	pod := corev1.PodTemplateSpec{
		Spec: corev1.PodSpec{
			RestartPolicy:    corev1.RestartPolicyNever,
			ImagePullSecrets: runner.Spec.ImagePullSecrets,
			Volumes:          []corev1.Volume{},
			InitContainers: []corev1.Container{
				{
					Name:            "worker",
					Image:           runner.Spec.Image,
					ImagePullPolicy: corev1.PullIfNotPresent,
					Command:         runner.Spec.Command,
					Env:             runner.Spec.Env,
				},
			},
			Containers: []corev1.Container{},
		},
	}
	// 强制注入 mc，无论 Upload.Enabled
	ensureUploadInjectionForPodSpec(&pod.Spec, &atopv1alpha1.Executor{Spec: atopv1alpha1.ExecutorSpec{Artifacts: runner.Spec.Artifacts, Storage: runner.Spec.Storage}}, false)
	return pod
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
			// Render full PodTemplate from Spec and annotate with spec hash
			backoff := int32(0)
			if runner.Spec.BackoffLimit != nil {
				backoff = *runner.Spec.BackoffLimit
			}
			pod := buildDesiredRunnerTemplate(runner)
			if pod.Annotations == nil {
				pod.Annotations = map[string]string{}
			}
			pod.Annotations[annotationSpecHashRunner] = computeSpecHashForRunner(runner, false)

			job = &batchv1.Job{
				ObjectMeta: metav1.ObjectMeta{
					Name:            runner.Name,
					Namespace:       runner.Namespace,
					Labels:          map[string]string{"app.kubernetes.io/managed-by": "tink-operator"},
					OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(runner, atopv1alpha1.GroupVersion.WithKind("Runner"))},
				},
				Spec: batchv1.JobSpec{BackoffLimit: &backoff, Template: pod},
			}
			if err := r.Create(ctx, job); err != nil {
				logger.Error(err, "failed to create Job")
				return ctrl.Result{}, err
			}
			r.Recorder.Eventf(runner, corev1.EventTypeNormal, reasonCreateJob, "created Job %s/%s", job.Namespace, job.Name)
			return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
		}
		return ctrl.Result{}, err
	}

	// If exists, check spec hash to decide whether to delete and recreate
	oldHash := ""
	if job.Spec.Template.Annotations != nil {
		oldHash = job.Spec.Template.Annotations[annotationSpecHashRunner]
	}
	newTpl := buildDesiredRunnerTemplate(runner)
	if newTpl.Annotations == nil {
		newTpl.Annotations = map[string]string{}
	}
	newHash := computeSpecHashForRunner(runner, false)
	newTpl.Annotations[annotationSpecHashRunner] = newHash
	if oldHash != newHash {
		// if job is still running, defer recreation
		if job.Status.Active > 0 {
			r.Recorder.Eventf(runner, corev1.EventTypeNormal, reasonDeferRecreate, "job is active, defer recreate until completion: oldHash=%s newHash=%s", oldHash, newHash)
			return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
		}
		// Delete old Job and let the next reconcile recreate it
		if err := r.Delete(ctx, job); err != nil {
			return ctrl.Result{}, err
		}
		r.Recorder.Eventf(runner, corev1.EventTypeNormal, reasonDeleteJobForSpecChange, "deleted Job for spec change: oldHash=%s newHash=%s", oldHash, newHash)
		return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
	}

	phase := "Pending"
	for i := range job.Status.Conditions {
		c := job.Status.Conditions[i]
		if c.Type == batchv1.JobComplete && c.Status == corev1.ConditionTrue {
			phase = "Succeeded"
			break
		}
		if c.Type == batchv1.JobFailed && c.Status == corev1.ConditionTrue {
			phase = "Failed"
			break
		}
	}
	if phase == "Pending" {
		if job.Status.Succeeded > 0 {
			phase = "Succeeded"
		} else if job.Status.Failed > 0 {
			phase = "Failed"
		} else if job.Status.Active > 0 {
			phase = "Running"
		}
	}

	newPhase := phase
	newReportStatus := "Waiting"
	newReportURL := ""
	switch newPhase {
	case "Succeeded":
		newReportStatus = "Succeeded"
		// ReportURL generated by external component or gateway
	case "Running":
		newReportStatus = "Uploading"
	default:
		newReportStatus = "Waiting"
	}

	latest := &atopv1alpha1.Runner{}
	if err := r.Get(ctx, req.NamespacedName, latest); err == nil {
		latest.Status.Phase = newPhase
		latest.Status.ReportStatus = newReportStatus
		latest.Status.ReportURL = newReportURL
		_ = r.Status().Update(ctx, latest)
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
