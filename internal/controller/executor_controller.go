package controller

import (
	"context"
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

	// 注入共享卷与 mc（Executor 使用 watch 模式）
	EnsureUploadInjectionForPodSpec(&deploySpec.Template.Spec, ex.Spec.Artifacts, ex.Spec.Storage, true, true)

	return &appsv1.Deployment{
		ObjectMeta: BuildObjectMeta(ex.Name, ex.Namespace, ex, "Executor"),
		Spec:       *deploySpec,
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
	logger := log.FromContext(ctx)

	ex := &atopv1alpha1.Executor{}
	if err := r.Get(ctx, req.NamespacedName, ex); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		logger.Error(err, "failed to get Executor")
		return ctrl.Result{}, err
	}

	dep := &appsv1.Deployment{}
	if err := r.Get(ctx, client.ObjectKey{Namespace: ex.Namespace, Name: ex.Name}, dep); err != nil {
		if errors.IsNotFound(err) {
			dep = buildDesiredExecutorDeployment(ex)
			if err := r.Create(ctx, dep); err != nil {
				logger.Error(err, "Failed to create Deployment for Executor", "executor", ex.Name)
				return ctrl.Result{}, err
			}
			r.Recorder.Eventf(ex, corev1.EventTypeNormal, reasonCreateDeployment,
				"Created Deployment %s/%s", dep.Namespace, dep.Name)
			logger.Info("Created Deployment for Executor", "executor", ex.Name, "deployment", dep.Name)
			return ctrl.Result{}, nil
		}
		logger.Error(err, "Failed to get Deployment", "executor", ex.Name)
		return ctrl.Result{}, err
	}

	// 检查并更新 Deployment
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
	newPhase, report := AggregateExecutorPhase(dep)
	if ex.Status.Phase != atopv1alpha1.ExecutorPhase(newPhase) || ex.Status.ReportStatus != report {
		logger.Info("Updating Executor status",
			"executor", ex.Name,
			"oldPhase", ex.Status.Phase,
			"newPhase", newPhase,
			"report", report,
		)
		ex.Status.Phase = atopv1alpha1.ExecutorPhase(newPhase)
		ex.Status.ReportStatus = report
		if err := r.Status().Update(ctx, ex); err != nil {
			logger.Error(err, "Failed to update Executor status", "executor", ex.Name)
			return ctrl.Result{}, err
		}
	}

	return ctrl.Result{}, nil
}

func (r *ExecutorReconciler) SetupWithManager(mgr ctrl.Manager) error {
	r.Recorder = mgr.GetEventRecorderFor("executor-controller")
	return ctrl.NewControllerManagedBy(mgr).
		For(&atopv1alpha1.Executor{}).
		Owns(&appsv1.Deployment{}).
		Complete(r)
}
