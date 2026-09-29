package battleai

import (
	"testing"

	"qqtang/internal/game/battleengine"
)

type fixedSampleCandidates []battleengine.ScoredAction

func (candidates fixedSampleCandidates) CandidateActions(_ battleengine.Observation, _ []battleengine.Action, limit int) ([]battleengine.ScoredAction, error) {
	if limit > len(candidates) {
		limit = len(candidates)
	}
	return append([]battleengine.ScoredAction(nil), candidates[:limit]...), nil
}

func TestSampledCandidatePolicyRandomizesOnlyNearTies(t *testing.T) {
	up, _ := battleengine.ActionFromID(1, battleengine.ActionMoveUp)
	down, _ := battleengine.ActionFromID(1, battleengine.ActionMoveDown)
	wait, _ := battleengine.ActionFromID(1, battleengine.ActionWait)
	legal := []battleengine.Action{wait, up, down}
	choose := func(policy SampledCandidatePolicy, draws int) (map[battleengine.ActionID]int, []battleengine.ActionID) {
		counts := map[battleengine.ActionID]int{}
		var sequence []battleengine.ActionID
		for draw := 0; draw < draws; draw++ {
			action, err := policy.ChooseAction(battleengine.Observation{}, legal)
			if err != nil {
				t.Fatal(err)
			}
			id, _ := action.ID()
			counts[id]++
			sequence = append(sequence, id)
		}
		return counts, sequence
	}
	// Down is exp(-0.2) = 0.82 of up; wait is exp(-5) = 0.007 of up, below min-p 0.1.
	nearTie := fixedSampleCandidates{{Action: up, Score: 3}, {Action: down, Score: 2.8}, {Action: wait, Score: -2}}
	counts, first := choose(SampledCandidatePolicy{Candidate: nearTie, MinP: 0.1, Temperature: 1, Rand: NewCandidateRand(7)}, 2000)
	if counts[battleengine.ActionWait] != 0 || counts[battleengine.ActionMoveDown] < 800 || counts[battleengine.ActionMoveDown] > 1000 {
		t.Fatalf("near-tie draws %v, want about 1100 up / 900 down and no wait", counts)
	}
	_, again := choose(SampledCandidatePolicy{Candidate: nearTie, MinP: 0.1, Temperature: 1, Rand: NewCandidateRand(7)}, 2000)
	for index := range first {
		if first[index] != again[index] {
			t.Fatalf("draw %d differs under the same seed", index)
		}
	}
	confident := fixedSampleCandidates{{Action: up, Score: 9}, {Action: down, Score: 2}}
	if counts, _ := choose(SampledCandidatePolicy{Candidate: confident, MinP: 0.1, Rand: NewCandidateRand(7)}, 500); counts[battleengine.ActionMoveUp] != 500 {
		t.Fatalf("a confident actor must stay greedy, got %v", counts)
	}
}

type refugeSampleCandidates struct {
	fixedSampleCandidates
	refuge []bool
}

func (candidates refugeSampleCandidates) lastRefugeActions() []bool { return candidates.refuge }

func TestSampledCandidatePolicyKeepsNearTiesWithoutRefugeOutOfTheDraw(t *testing.T) {
	up, _ := battleengine.ActionFromID(1, battleengine.ActionMoveUp)
	down, _ := battleengine.ActionFromID(1, battleengine.ActionMoveDown)
	legal := []battleengine.Action{up, down}
	refuge := make([]bool, battleengine.DiscreteActionCount)
	refuge[battleengine.ActionMoveDown] = true // up (the best action) has no refuge; it stays eligible anyway
	nearTie := refugeSampleCandidates{fixedSampleCandidates{{Action: up, Score: 3}, {Action: down, Score: 2.9}}, refuge}
	policy := SampledCandidatePolicy{Candidate: nearTie, MinP: 0.1, Rand: NewCandidateRand(3)}
	counts := map[battleengine.ActionID]int{}
	for draw := 0; draw < 400; draw++ {
		action, err := policy.ChooseAction(battleengine.Observation{}, legal)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := action.ID()
		counts[id]++
	}
	if counts[battleengine.ActionMoveUp] == 0 || counts[battleengine.ActionMoveDown] == 0 {
		t.Fatalf("both refuge-keeping near tie and best action should be drawn, got %v", counts)
	}
	refuge[battleengine.ActionMoveDown] = false
	for draw := 0; draw < 200; draw++ {
		action, _ := policy.ChooseAction(battleengine.Observation{}, legal)
		if id, _ := action.ID(); id != battleengine.ActionMoveUp {
			t.Fatalf("a near tie without refuge was drawn: %d", id)
		}
	}
}

func TestRefugeActionsReadsTheEngineProjection(t *testing.T) {
	scalars := make([]float32, bombRefugeFoundScalar+refugeDirections)
	scalars[currentRefugeFoundScalar+2] = 1 // move right keeps a refuge
	scalars[bombRefugeFoundScalar+3] = 1    // bubble then down would, but the projection is invalid
	safe := refugeActions(scalars, int(battleengine.DiscreteActionCount))
	if !safe[2] || safe[0] || safe[8] || !safe[10] {
		t.Fatalf("refuge mask %v", safe[:11])
	}
	scalars[bombProjectionValidScalar] = 1
	if safe = refugeActions(scalars, int(battleengine.DiscreteActionCount)); !safe[8] || safe[5] {
		t.Fatalf("valid bubble projection mask %v", safe[:11])
	}
}
