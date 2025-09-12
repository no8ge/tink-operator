package v1alpha1

import (
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ExecutorSpec defines the desired state of Executor
type ExecutorSpec struct {
	// 用户定义 Deployment，Operator 仅做最小注入
	appsv1.DeploymentSpec `json:",inline"`

	// 产物目录与对象存储，仅当两者同时配置时注入上传逻辑
	//+kubebuilder:validation:Optional
	Artifacts ArtifactsConfig `json:"artifacts,omitempty"`

	//+kubebuilder:validation:Optional
	Storage StorageConfig `json:"storage,omitempty"`
}

type ArtifactsConfig struct {
	// 产物目录路径，建议以 "/" 开头
	//+kubebuilder:validation:Optional
	//+kubebuilder:validation:Pattern=`^/.*`
	Path string `json:"path"`

	// 产物名称（可选）
	//+kubebuilder:validation:Optional
	Name string `json:"name"`
}

type StorageConfig struct {
	// MinIO 桶名
	//+kubebuilder:validation:Optional
	Bucket string `json:"bucket"`

	// 上传路径前缀
	//+kubebuilder:validation:Optional
	Prefix string `json:"prefix"`

	// MinIO 访问地址
	//+kubebuilder:validation:Optional
	MinioEndpoint string `json:"minioEndpoint"`

	// AccessKey/SecretKey 存储（明文/Secret 引用取决于实现）
	//+kubebuilder:validation:Optional
	AccessKeySecret string `json:"accessKeySecret"`

	//+kubebuilder:validation:Optional
	SecretKeySecret string `json:"secretKeySecret"`
}

// ExecutorPhase 表示 Executor 的生命周期阶段
// +kubebuilder:validation:Enum=Pending;Progressing;Available;Degraded
type ExecutorPhase string

const (
	ExecutorPending     ExecutorPhase = "Pending"
	ExecutorProgressing ExecutorPhase = "Progressing"
	ExecutorAvailable   ExecutorPhase = "Available"
	ExecutorDegraded    ExecutorPhase = "Degraded"
)

// ExecutorStatus defines the observed state of Executor
type ExecutorStatus struct {
	Phase              ExecutorPhase `json:"phase"`
	ReportStatus       string        `json:"reportStatus"`
	LastTransitionTime metav1.Time   `json:"lastTransitionTime,omitempty"`
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
