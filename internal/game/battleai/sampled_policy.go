package battleai

import (
	"fmt"
	"math"

	"qqtang/internal/game/battleengine"
)

// SampledCandidatePolicy runs the learned actor once and samples among its
// near-best legal actions: those whose softmax probability at Temperature is
// at least MinP times the best one's (min-p truncation). The best action is
// always eligible, so a confident actor stays greedy and only near-ties are
// randomized. Actors sharing one model then stop acting in lockstep, and the
// rule needs no retuning as training sharpens the policy. Every other
// candidate must also keep an engine-projected refuge when the candidate
// policy reports one (NeuralCandidates does): offline, sampling without that
// filter traded timeouts for self-eliminations. Rand is actor-local; seed it
// per match and player so a match replays exactly.
type SampledCandidatePolicy struct {
	Candidate   battleengine.CandidatePolicy
	MinP        float64
	Temperature float64
	Rand        *CandidateRand
}

func (policy SampledCandidatePolicy) ChooseAction(observation battleengine.Observation, legal []battleengine.Action) (battleengine.Action, error) {
	if policy.Candidate == nil {
		return battleengine.Action{}, fmt.Errorf("sampled policy has no candidate policy")
	}
	candidates, err := policy.Candidate.CandidateActions(observation, legal, len(legal))
	if err != nil {
		return battleengine.Action{}, err
	}
	return policy.sample(candidates)
}

func (policy SampledCandidatePolicy) ChooseActionWithSnapshot(snapshot *battleengine.Engine, observation battleengine.Observation, legal []battleengine.Action) (battleengine.Action, error) {
	if snapshot == nil {
		return battleengine.Action{}, fmt.Errorf("sampled policy received nil engine snapshot")
	}
	if policy.Candidate == nil {
		return battleengine.Action{}, fmt.Errorf("sampled policy has no candidate policy")
	}
	var candidates []battleengine.ScoredAction
	var err error
	if snapshotCandidate, ok := policy.Candidate.(battleengine.SnapshotCandidatePolicy); ok {
		candidates, err = snapshotCandidate.CandidateActionsWithSnapshot(snapshot, observation, legal, len(legal))
	} else {
		candidates, err = policy.Candidate.CandidateActions(observation, legal, len(legal))
	}
	if err != nil {
		return battleengine.Action{}, err
	}
	return policy.sample(candidates)
}

func (policy SampledCandidatePolicy) sample(candidates []battleengine.ScoredAction) (battleengine.Action, error) {
	if len(candidates) == 0 {
		return battleengine.Action{}, fmt.Errorf("candidate policy returned no legal action")
	}
	if policy.Rand == nil {
		return battleengine.Action{}, fmt.Errorf("sampled policy has no random source")
	}
	temperature := policy.Temperature
	if temperature <= 0 {
		temperature = 1
	}
	best := 0
	for index, candidate := range candidates {
		if candidate.Score > candidates[best].Score {
			best = index
		}
	}
	var refuge []bool
	if source, ok := policy.Candidate.(refugeReporter); ok {
		refuge = source.lastRefugeActions()
	}
	// p_i / p_best = exp((s_i - s_best) / T); no full softmax is needed.
	weights := make([]float64, len(candidates))
	total := 0.0
	for index, candidate := range candidates {
		ratio := math.Exp(float64(candidate.Score-candidates[best].Score) / temperature)
		if index == best || (ratio >= policy.MinP && keepsRefuge(refuge, candidate.Action)) {
			weights[index] = ratio
			total += ratio
		}
	}
	draw := policy.Rand.Float64() * total
	for index, weight := range weights {
		if weight == 0 {
			continue
		}
		if draw < weight {
			return candidates[index].Action, nil
		}
		draw -= weight
	}
	return candidates[best].Action, nil
}

// refugeReporter is implemented by candidate policies that know, per action
// ID, whether the last decision's engine projection keeps a refuge.
type refugeReporter interface {
	lastRefugeActions() []bool
}

func keepsRefuge(refuge []bool, action battleengine.Action) bool {
	if refuge == nil {
		return true
	}
	id, ok := action.ID()
	return ok && int(id) < len(refuge) && refuge[id]
}

// CandidateRand is a small deterministic generator (SplitMix64) for
// SampledCandidatePolicy, so live sampling is replayable from its seed.
type CandidateRand struct {
	state uint64
}

func NewCandidateRand(seed uint64) *CandidateRand {
	return &CandidateRand{state: seed}
}

func (random *CandidateRand) Float64() float64 {
	random.state += 0x9E3779B97F4A7C15
	value := random.state
	value = (value ^ (value >> 30)) * 0xBF58476D1CE4E5B9
	value = (value ^ (value >> 27)) * 0x94D049BB133111EB
	value ^= value >> 31
	return float64(value>>11) / (1 << 53)
}
