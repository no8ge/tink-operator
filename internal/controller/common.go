package controller

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"

	corev1 "k8s.io/api/core/v1"
)

const (
	annotationSpecHash = "autotest.atop.io/spec-hash"
	sharedVolumeName   = "shared-data"
	mcContainerName    = "mc"
	defaultReportPath  = "/data"

	// event reasons
	reasonCreateDeployment       = "CreateDeployment"
	reasonUpdateDeploymentTpl    = "UpdateDeploymentTemplate"
	reasonCreateJob              = "CreateJob"
	reasonDeferRecreate          = "DeferRecreate"
	reasonDeleteJobForSpecChange = "DeleteJobForSpecChange"
)

// computeSHA256Hex marshals the input to JSON (assumes already normalized/sorted) and returns hex-encoded SHA256
func computeSHA256Hex(v interface{}) string {
	b, _ := json.Marshal(v)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func normalizeReportPath(path string) string {
	if path == "" {
		return defaultReportPath
	}
	return path
}

func sortedEnv(env []corev1.EnvVar) []corev1.EnvVar {
	out := make([]corev1.EnvVar, len(env))
	copy(out, env)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func sortedPorts(ports []corev1.ContainerPort) []corev1.ContainerPort {
	out := make([]corev1.ContainerPort, len(ports))
	copy(out, ports)
	sort.Slice(out, func(i, j int) bool {
		if out[i].ContainerPort == out[j].ContainerPort {
			return out[i].Name < out[j].Name
		}
		return out[i].ContainerPort < out[j].ContainerPort
	})
	return out
}

func sortedImagePullSecrets(secrets []corev1.LocalObjectReference) []corev1.LocalObjectReference {
	out := make([]corev1.LocalObjectReference, len(secrets))
	copy(out, secrets)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
