package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// RunnerSpec defines the desired state of Runner (one-shot Job)
type RunnerSpec struct {
	// 简化字段：由控制器生成 Job
	Image            string                        `json:"image"`
	Command          []string                      `json:"command,omitempty"`
	Env              []corev1.EnvVar               `json:"env,omitempty"`
	ImagePullSecrets []corev1.LocalObjectReference `json:"imagePullSecrets,omitempty"`
	BackoffLimit     *int32                        `json:"backoffLimit,omitempty"`

	// 契约：artifacts.root（必填）
	Artifacts ArtifactsConfig `json:"artifacts,omitempty"`
	// 契约：artifacts 目标存储（必填）
	Storage StorageConfig `json:"storage,omitempty"`
}

// RunnerStatus defines the observed state of Runner
type RunnerStatus struct {
	Phase        string `json:"phase"`
	ReportStatus string `json:"reportStatus"`
	ReportURL    string `json:"reportURL"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status

// Runner is the Schema for the runners API
type Runner struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   RunnerSpec   `json:"spec,omitempty"`
	Status RunnerStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// RunnerList contains a list of Runner
type RunnerList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Runner `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Runner{}, &RunnerList{})
}
