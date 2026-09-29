package battleai

import (
	"fmt"
	"sort"

	"qqtang/internal/game/battleengine"
	"qqtang/internal/game/battleenv"
)

// LogitRunner is the narrow boundary used by ONNX Runtime and test doubles;
// inference implementations do not own combat semantics.
type LogitRunner interface {
	RunActor(spatial []float32, scalars []float32, legal []uint8) ([]float32, error)
}

// RecurrentLogitRunner advances caller-owned actor memory. Implementations
// must not retain the slice: one shared inference session serves many rooms,
// while each actor policy owns and commits only its returned state.
type RecurrentLogitRunner interface {
	RunActorRecurrent(
		spatial []float32,
		scalars []float32,
		legal []uint8,
		memory []float32,
		reset bool,
	) (logits []float32, nextMemory []float32, err error)
}

// NeuralCandidates adapts actor logits to battleengine.TopKSearchPolicy.
type NeuralCandidates struct {
	Contract        Contract
	Runner          LogitRunner
	DangerHorizonMS uint32
	DecisionMS      uint32
	memory          []float32
	resetMemory     bool
	// refuge holds, per action ID, whether the engine projection of the last
	// encoded decision keeps a whole-cell refuge (see refugeActions).
	refuge []bool
}

// Scalar layout written by battleenv.setTacticalConsequenceFeatures: whole-cell
// refuge found per movement direction (stay, up, right, down, left) in the
// current world and after placing a bubble; the latter only counts when the
// bubble projection is valid.
const (
	bombProjectionValidScalar = 95
	currentRefugeFoundScalar  = 162
	bombRefugeFoundScalar     = 172
	refugeDirections          = 5
)

// refugeActions marks stay/move (IDs 0..4) and bubble-plus-move (5..9)
// actions whose projection keeps a refuge. Other actions (items) stay open.
// The projection covers known danger, not future enemy play.
func refugeActions(scalars []float32, actions int) []bool {
	safe := make([]bool, actions)
	for id := range safe {
		safe[id] = true
	}
	if len(scalars) < bombRefugeFoundScalar+refugeDirections || actions < 2*refugeDirections {
		return safe
	}
	for direction := 0; direction < refugeDirections; direction++ {
		safe[direction] = scalars[currentRefugeFoundScalar+direction] > 0.5
		safe[refugeDirections+direction] = scalars[bombRefugeFoundScalar+direction] > 0.5 &&
			scalars[bombProjectionValidScalar] > 0.5
	}
	return safe
}

// lastRefugeActions is the refuge mask of the most recent decision.
func (policy *NeuralCandidates) lastRefugeActions() []bool {
	return policy.refuge
}

func (policy *NeuralCandidates) CandidateActions(
	_ battleengine.Observation,
	_ []battleengine.Action,
	_ int,
) ([]battleengine.ScoredAction, error) {
	return nil, fmt.Errorf("neural candidate policy requires an engine snapshot for danger features")
}

func (policy *NeuralCandidates) CandidateActionsWithSnapshot(
	snapshot *battleengine.Engine,
	observation battleengine.Observation,
	legalActions []battleengine.Action,
	limit int,
) ([]battleengine.ScoredAction, error) {
	if snapshot == nil || policy.Runner == nil {
		return nil, fmt.Errorf("neural candidate policy is not initialized")
	}
	if err := policy.Contract.Validate(); err != nil {
		return nil, err
	}
	horizon := policy.DangerHorizonMS
	if horizon == 0 {
		horizon = 3_500
	}
	danger, err := snapshot.DangerTimeline(horizon)
	if err != nil {
		return nil, err
	}
	legalMask, err := snapshot.LegalActionMask(observation.PlayerID)
	if err != nil {
		return nil, err
	}
	encoded, err := battleenv.EncodeActorAtDecision(
		snapshot, observation, danger, legalMask, policy.Contract.Height, policy.Contract.Width,
		policy.DecisionMS,
	)
	if err != nil {
		return nil, err
	}
	// Schema 17 only appends terrain planes; keep existing model inputs intact.
	encoded.Spatial = encoded.Spatial[:policy.Contract.Channels*policy.Contract.Height*policy.Contract.Width]
	policy.refuge = refugeActions(encoded.ScalarValues, policy.Contract.Actions)
	var logits []float32
	if policy.Contract.RecurrentHiddenSize > 0 {
		runner, ok := policy.Runner.(RecurrentLogitRunner)
		if !ok {
			return nil, fmt.Errorf("AI contract requires recurrent inference but backend is stateless")
		}
		if len(policy.memory) == 0 {
			policy.memory = make([]float32, policy.Contract.RecurrentHiddenSize)
			policy.resetMemory = true
		}
		var nextMemory []float32
		logits, nextMemory, err = runner.RunActorRecurrent(
			encoded.Spatial, encoded.ScalarValues, encoded.Legal,
			policy.memory, policy.resetMemory,
		)
		if err == nil {
			if len(nextMemory) != policy.Contract.RecurrentHiddenSize {
				return nil, fmt.Errorf(
					"learned actor returned %d recurrent values, want %d",
					len(nextMemory), policy.Contract.RecurrentHiddenSize,
				)
			}
			copy(policy.memory, nextMemory)
			policy.resetMemory = false
		}
	} else {
		logits, err = policy.Runner.RunActor(
			encoded.Spatial, encoded.ScalarValues, encoded.Legal,
		)
	}
	if err != nil {
		return nil, fmt.Errorf("run learned actor: %w", err)
	}
	if len(logits) != policy.Contract.Actions {
		return nil, fmt.Errorf("learned actor returned %d logits, want %d", len(logits), policy.Contract.Actions)
	}
	legalByID := make(map[battleengine.ActionID]battleengine.Action, len(legalActions))
	for _, action := range legalActions {
		id, ok := action.ID()
		if !ok {
			return nil, fmt.Errorf("legal action %+v has no stable ID", action)
		}
		legalByID[id] = action
	}
	result := make([]battleengine.ScoredAction, 0, len(legalByID))
	for id, action := range legalByID {
		result = append(result, battleengine.ScoredAction{Action: action, Score: logits[id]})
	}
	sort.SliceStable(result, func(i, j int) bool {
		if result[i].Score == result[j].Score {
			left, _ := result[i].Action.ID()
			right, _ := result[j].Action.ID()
			return left < right
		}
		return result[i].Score > result[j].Score
	})
	if limit <= 0 || limit > len(result) {
		limit = len(result)
	}
	return result[:limit], nil
}
