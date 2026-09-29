package battleai

import (
	"fmt"
	"sync"

	"qqtang/internal/game/battleengine"
)

// ActorPolicyConfig describes the server's actor-local policy wrapper.
// Greedy actor inference is the deployment default so formal evaluation and
// live execution use the same learned decisions. Bounded search remains an
// explicit diagnostic/teacher option rather than an implicit safety layer.
// Live participants sample instead when the model's contract enables it
// (Contract.Sampling); an enabled search takes precedence.
type ActorPolicyConfig struct {
	DangerHorizonMS uint32
	DecisionMS      uint32
	EnableSearch    bool
	Search          battleengine.SearchConfig
}

func buildActorPolicy(
	contract Contract,
	runner LogitRunner,
	config ActorPolicyConfig,
) (battleengine.Policy, error) {
	if err := contract.Validate(); err != nil {
		return nil, fmt.Errorf("validate AI contract: %w", err)
	}
	template := &actorPolicyTemplate{
		contract: contract, runner: runner, config: config,
	}
	template.defaultPolicy = template.NewActorPolicy()
	return template, nil
}

// actorPolicyTemplate owns immutable model/session resources and creates the
// actor-local wrapper that owns recurrent memory. The default delegate keeps
// direct single-actor callers working; live matches always fork one delegate
// per virtual participant.
type actorPolicyTemplate struct {
	contract      Contract
	runner        LogitRunner
	config        ActorPolicyConfig
	defaultMu     sync.Mutex
	defaultPolicy battleengine.Policy
}

func (template *actorPolicyTemplate) newCandidates() *NeuralCandidates {
	return &NeuralCandidates{
		Contract:        template.contract,
		Runner:          template.runner,
		DangerHorizonMS: template.config.DangerHorizonMS,
		DecisionMS:      template.config.DecisionMS,
		resetMemory:     template.contract.RecurrentHiddenSize > 0,
	}
}

func (template *actorPolicyTemplate) NewActorPolicy() battleengine.Policy {
	candidates := template.newCandidates()
	if !template.config.EnableSearch {
		return battleengine.GreedyCandidatePolicy{Candidate: candidates}
	}
	return battleengine.TopKSearchPolicy{
		Candidate: candidates, Config: template.config.Search,
	}
}

// NewSeededActorPolicy is NewActorPolicy for a live participant: when the
// contract enables sampling (and search is off) it samples near-best actions
// from an actor-local generator seeded by the caller (match and player).
func (template *actorPolicyTemplate) NewSeededActorPolicy(seed uint64) battleengine.Policy {
	if !template.contract.Sampled() || template.config.EnableSearch {
		return template.NewActorPolicy()
	}
	return SampledCandidatePolicy{
		Candidate:   template.newCandidates(),
		MinP:        template.contract.Sampling.MinP,
		Temperature: template.contract.Sampling.Temperature,
		Rand:        NewCandidateRand(seed),
	}
}

func (template *actorPolicyTemplate) ChooseAction(
	observation battleengine.Observation,
	legal []battleengine.Action,
) (battleengine.Action, error) {
	template.defaultMu.Lock()
	defer template.defaultMu.Unlock()
	return template.defaultPolicy.ChooseAction(observation, legal)
}

func (template *actorPolicyTemplate) ChooseActionWithSnapshot(
	snapshot *battleengine.Engine,
	observation battleengine.Observation,
	legal []battleengine.Action,
) (battleengine.Action, error) {
	template.defaultMu.Lock()
	defer template.defaultMu.Unlock()
	if policy, ok := template.defaultPolicy.(battleengine.SnapshotPolicy); ok {
		return policy.ChooseActionWithSnapshot(snapshot, observation, legal)
	}
	return template.defaultPolicy.ChooseAction(observation, legal)
}
