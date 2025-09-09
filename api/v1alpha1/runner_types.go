package v1alpha1

import (
	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// RunnerSpec defines the desired state of Runner (one-shot Job)
type RunnerSpec struct {
	batchv1.JobSpec `json:",inline"`

	Artifacts ArtifactsConfig `json:"artifacts,omitempty"`
	Storage   StorageConfig   `json:"storage,omitempty"`
}

// RunnerStatus defines the observed state of Runner
type RunnerPhase string

const (
	RunnerPending   RunnerPhase = "Pending"
	RunnerRunning   RunnerPhase = "Running"
	RunnerSucceeded RunnerPhase = "Succeeded"
	RunnerFailed    RunnerPhase = "Failed"
)

type RunnerStatus struct {
	Phase        RunnerPhase `json:"phase"`
	ReportStatus string      `json:"reportStatus,omitempty"`
	ReportURL    string      `json:"reportURL,omitempty"`
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
