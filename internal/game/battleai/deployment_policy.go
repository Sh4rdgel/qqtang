package battleai

import (
	"fmt"
	"io"
	"strings"

	"qqtang/internal/game/battleengine"
)

const (
	DeploymentBackendONNXRuntime = "onnxruntime"
)

// DeploymentPolicyConfig selects one inference implementation at process
// startup. It never changes the authoritative combat policy or switches
// numerical backends between frames.
type DeploymentPolicyConfig struct {
	Backend           string
	ModelPath         string
	MetadataPath      string
	SharedLibraryPath string
	IntraOpThreads    int
	InterOpThreads    int
	ActorPolicyConfig ActorPolicyConfig
}

// LoadedPolicy owns any backend resources needed by a server process.
type LoadedPolicy struct {
	Policy   battleengine.Policy
	Backend  string
	Contract Contract
	Closer   io.Closer
}

// LoadDeploymentPolicy is deliberately strict: a requested ONNX Runtime
// backend must initialize successfully; packaging failures are startup errors.
func LoadDeploymentPolicy(config DeploymentPolicyConfig) (LoadedPolicy, error) {
	backend := strings.ToLower(strings.TrimSpace(config.Backend))
	if backend == "" {
		backend = DeploymentBackendONNXRuntime
	}
	var (
		runner   LogitRunner
		contract Contract
		closer   io.Closer
		err      error
	)
	switch backend {
	case DeploymentBackendONNXRuntime:
		runner, contract, closer, err = loadONNXRuntimeDeploymentRunner(config)
	default:
		return LoadedPolicy{}, fmt.Errorf("unsupported AI deployment backend %q", backend)
	}
	if err != nil {
		return LoadedPolicy{}, fmt.Errorf("load %s AI backend: %w", backend, err)
	}
	policy, err := buildActorPolicy(contract, runner, config.ActorPolicyConfig)
	if err != nil {
		if closer != nil {
			_ = closer.Close()
		}
		return LoadedPolicy{}, err
	}
	return LoadedPolicy{
		Policy: policy, Backend: backend, Contract: contract, Closer: closer,
	}, nil
}
