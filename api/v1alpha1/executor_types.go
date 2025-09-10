package v1alpha1

import (
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ExecutorSpec defines the desired state of Executor
type ExecutorSpec struct {
	// 原生 DeploymentSpec，用户定义工作负载，Operator 仅做最小注入
	appsv1.DeploymentSpec `json:",inline"`

	// 产物目录与对象存储，仅当两者同时配置时注入上传逻辑
	Artifacts ArtifactsConfig `json:"artifacts,omitempty"`
	Storage   StorageConfig   `json:"storage,omitempty"`
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
