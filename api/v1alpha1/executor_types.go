package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ExecutorSpec defines the desired state of Executor
type ExecutorSpec struct {
	// 简化字段：由控制器生成 Deployment
	Image            string                        `json:"image"`
	Command          []string                      `json:"command,omitempty"`
	Env              []corev1.EnvVar               `json:"env,omitempty"`
	ImagePullSecrets []corev1.LocalObjectReference `json:"imagePullSecrets,omitempty"`
	Replicas         *int32                        `json:"replicas,omitempty"`
	Resources        corev1.ResourceRequirements   `json:"resources,omitempty"`
	Ports            []corev1.ContainerPort        `json:"ports,omitempty"`

	// 契约：artifacts.root
	// 说明：测试/服务产物根目录；仅当与 Storage 一并配置时，控制器才注入上传 sidecar。
	Artifacts ArtifactsConfig `json:"artifacts,omitempty"`
	// 契约：artifacts 目标存储
	// 说明：对象存储目标（endpoint/bucket/prefix/凭证）；仅当与 Artifacts.Path 一并配置时启用上传。
	Storage StorageConfig `json:"storage,omitempty"`
}

type ArtifactsConfig struct {
	Path string `json:"path"`
	Name string `json:"name"`
}

type StorageConfig struct {
	Bucket          string `json:"bucket"`
	Prefix          string `json:"prefix"`
	MinioEndpoint   string `json:"minioEndpoint"`
	AccessKeySecret string `json:"accessKeySecret"`
	SecretKeySecret string `json:"secretKeySecret"`
}

// ExecutorStatus defines the observed state of Executor
type ExecutorStatus struct {
	Phase        string `json:"phase"`
	ReportStatus string `json:"reportStatus"`
	ReportURL    string `json:"reportURL"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status

// Executor is the Schema for the executors API
type Executor struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ExecutorSpec   `json:"spec,omitempty"`
	Status ExecutorStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// ExecutorList contains a list of Executor
type ExecutorList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Executor `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Executor{}, &ExecutorList{})
}
