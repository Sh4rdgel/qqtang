package battleai

import (
	"strings"
	"testing"
)

func TestDeploymentDefaultsToONNXAndRejectsRemovedBackend(t *testing.T) {
	_, err := LoadDeploymentPolicy(DeploymentPolicyConfig{ModelPath: "missing.onnx"})
	if err == nil || !strings.Contains(err.Error(), "load onnxruntime AI backend") {
		t.Fatalf("default backend: %v", err)
	}
	_, err = LoadDeploymentPolicy(DeploymentPolicyConfig{Backend: "native"})
	if err == nil || !strings.Contains(err.Error(), "unsupported AI deployment backend") {
		t.Fatalf("removed backend: %v", err)
	}
}
