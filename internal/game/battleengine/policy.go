package battleengine

import (
	"fmt"
	"sort"
	"sync"
)

// Policy chooses one already-legal action for a virtual participant. The
// engine supplies an immutable observation and a stable legal-action list;
// implementations must not own gameplay state. This small interface is shared
// by live server bots, scripted baselines and later learned policies.
type Policy interface {
	ChooseAction(observation Observation, legal []Action) (Action, error)
}

// SnapshotPolicy is an optional policy boundary for public features that need
// engine timing (for example DangerTimeline) and for bounded tactical search.
// Runtime passes a deep copy, never the authoritative engine, so a policy
// cannot mutate live state. The ordinary observation remains the actor's
// visibility boundary; implementations must not score hidden pickups or other
// private map data from the snapshot.
type SnapshotPolicy interface {
	ChooseActionWithSnapshot(snapshot *Engine, observation Observation, legal []Action) (Action, error)
}

// PolicyFunc adapts a function into Policy.
type PolicyFunc func(observation Observation, legal []Action) (Action, error)

func (function PolicyFunc) ChooseAction(observation Observation, legal []Action) (Action, error) {
	return function(observation, legal)
}

// Runtime binds virtual participants to policies while leaving human input at
// the server boundary. It owns no clock or goroutine: one call advances exactly
// one Engine tick, so the same type can be driven by a live scheduler or an
// offline vectorized environment.
type Runtime struct {
	engine           *Engine
	humanPlayerIDs   []uint16
	virtualPlayerIDs []uint16
	policies         map[uint16]Policy
	suspendedVirtual map[uint16]struct{}
}

// RuntimeStepResult is one atomic runtime transition together with the final
// input applied to every participant. Actions are reported after runtime-level
// safety guards have adjusted policy proposals, so a live transport adapter
// never has to infer held input from the resulting position delta.
//
// That distinction matters at native collision boundaries: an actor holding a
// direction against a wall has a moving input segment even though its physical
// position does not change.
type RuntimeStepResult struct {
	Actions []Action
	Events  []Event
}

// NewRuntime requires exactly one policy for every virtual participant and
// rejects policies attached to humans or unknown IDs. This fail-closed binding
// prevents a caller from accidentally replacing live player input with AI.
func NewRuntime(engine *Engine, policies map[uint16]Policy) (*Runtime, error) {
	if engine == nil {
		return nil, fmt.Errorf("battle runtime requires an engine")
	}
	runtime := &Runtime{
		engine: engine, policies: make(map[uint16]Policy),
		suspendedVirtual: make(map[uint16]struct{}),
	}
	participants := make(map[uint16]ParticipantSource, len(engine.actors))
	for _, actor := range engine.actors {
		participants[actor.PlayerID] = actor.Source
		switch actor.Source {
		case ParticipantHuman:
			runtime.humanPlayerIDs = append(runtime.humanPlayerIDs, actor.PlayerID)
		case ParticipantVirtualAI:
			runtime.virtualPlayerIDs = append(runtime.virtualPlayerIDs, actor.PlayerID)
		default:
			return nil, fmt.Errorf("battle runtime player %d has invalid source %d", actor.PlayerID, actor.Source)
		}
	}
	for playerID, policy := range policies {
		source, exists := participants[playerID]
		if !exists {
			return nil, fmt.Errorf("battle runtime policy player %d is not a participant", playerID)
		}
		if source != ParticipantVirtualAI {
			return nil, fmt.Errorf("battle runtime policy player %d is not virtual", playerID)
		}
		if policy == nil {
			return nil, fmt.Errorf("battle runtime policy player %d is nil", playerID)
		}
		runtime.policies[playerID] = policy
	}
	for _, playerID := range runtime.virtualPlayerIDs {
		if runtime.policies[playerID] == nil {
			return nil, fmt.Errorf("battle runtime virtual player %d has no policy", playerID)
		}
	}
	sort.Slice(runtime.humanPlayerIDs, func(i, j int) bool { return runtime.humanPlayerIDs[i] < runtime.humanPlayerIDs[j] })
	sort.Slice(runtime.virtualPlayerIDs, func(i, j int) bool { return runtime.virtualPlayerIDs[i] < runtime.virtualPlayerIDs[j] })
	return runtime, nil
}

func (runtime *Runtime) HumanPlayerIDs() []uint16 {
	if runtime == nil {
		return nil
	}
	return append([]uint16(nil), runtime.humanPlayerIDs...)
}

func (runtime *Runtime) VirtualPlayerIDs() []uint16 {
	if runtime == nil {
		return nil
	}
	return append([]uint16(nil), runtime.virtualPlayerIDs...)
}

// SuspendVirtualActor installs or removes a live-authority quarantine without
// committing the hit outcome. While suspended, StepWithTrace applies
// a neutral action and does not invoke the policy. The live adapter uses this
// boundary after publishing an actor-hit request: the native scene has already
// stopped that participant, but the fixed mirror must remain Active until the
// elected authority returns the exact reliable audit.
func (runtime *Runtime) SuspendVirtualActor(playerID uint16, suspended bool) error {
	if runtime == nil || runtime.engine == nil {
		return fmt.Errorf("battle runtime is nil")
	}
	index := runtime.engine.actorIndex(playerID)
	if index < 0 || runtime.engine.actors[index].Source != ParticipantVirtualAI {
		return fmt.Errorf("battle runtime suspension player %d is not virtual", playerID)
	}
	runtime.engine.actors[index].nativeHitPending = suspended
	if suspended {
		runtime.suspendedVirtual[playerID] = struct{}{}
	} else {
		delete(runtime.suspendedVirtual, playerID)
	}
	return nil
}

// SuspendVirtualActorAtHit stops the missing native client's movement at its
// FA5 checkpoint without speculating about the authoritative hit outcome.
func (runtime *Runtime) SuspendVirtualActorAtHit(playerID uint16, position Position) error {
	if runtime == nil || runtime.engine == nil {
		return fmt.Errorf("battle runtime is nil")
	}
	index := runtime.engine.actorIndex(playerID)
	if index < 0 || runtime.engine.actors[index].Source != ParticipantVirtualAI {
		return fmt.Errorf("battle runtime hit player %d is not virtual", playerID)
	}
	if err := runtime.engine.ApplyVerifiedMovementCheckpoint(playerID, position); err != nil {
		return err
	}
	actor := &runtime.engine.actors[index]
	actor.Facing = DirectionDown
	actor.nativePreviousPosition = position
	actor.moveRemainder = 0
	return runtime.SuspendVirtualActor(playerID, true)
}

func (runtime *Runtime) VirtualActorSuspended(playerID uint16) bool {
	if runtime == nil {
		return false
	}
	_, suspended := runtime.suspendedVirtual[playerID]
	return suspended
}

// EngineSnapshot returns a deep copy suitable for diagnostics or search. Live
// callers cannot mutate the runtime's authoritative engine through this API.
func (runtime *Runtime) EngineSnapshot() *Engine {
	if runtime == nil || runtime.engine == nil {
		return nil
	}
	return runtime.engine.Clone()
}

// ReconcileHumanMovement applies an authenticated original-client absolute
// movement sample. This boundary is available only for human participants;
// virtual policies must continue to advance exclusively through Step.
func (runtime *Runtime) ReconcileHumanMovement(playerID uint16, position Position) error {
	if runtime == nil || runtime.engine == nil {
		return fmt.Errorf("battle runtime is nil")
	}
	index := runtime.engine.actorIndex(playerID)
	if index < 0 || runtime.engine.actors[index].Source != ParticipantHuman {
		return fmt.Errorf("battle runtime movement player %d is not human", playerID)
	}
	return runtime.engine.ApplyVerifiedMovementCheckpoint(playerID, position)
}

// ReconcileHumanMovementState additionally preserves the actor's last native
// facing direction. Absolute position remains authoritative; the direction is
// used only by observation/capability rules whose original client state is
// orientation-dependent (for example transformed asymmetric movement).
func (runtime *Runtime) ReconcileHumanMovementState(playerID uint16, position Position, direction Direction) error {
	if _, _, ok := direction.delta(); !ok {
		return fmt.Errorf("battle runtime movement player %d has invalid direction %d", playerID, direction)
	}
	if err := runtime.ReconcileHumanMovement(playerID, position); err != nil {
		return err
	}
	if direction != DirectionNone {
		index := runtime.engine.actorIndex(playerID)
		runtime.engine.actors[index].Facing = direction
	}
	return nil
}

// AcceptHumanBombPlacement installs an authenticated original-client bomb in
// the shared engine. The event has already been produced and delivered by the
// human client, so live projection must never echo the returned event.
func (runtime *Runtime) AcceptHumanBombPlacement(playerID uint16, cell Cell, power uint16, property byte, placedAtMS uint32) (Event, error) {
	if runtime == nil || runtime.engine == nil {
		return Event{}, fmt.Errorf("battle runtime is nil")
	}
	index := runtime.engine.actorIndex(playerID)
	if index < 0 || runtime.engine.actors[index].Source != ParticipantHuman {
		return Event{}, fmt.Errorf("battle runtime bomb player %d is not human", playerID)
	}
	// PLAYER_USE_BOMB serializes the client's internal blast reach plus one:
	// a native wire value of 2 produces the centre cell plus one arm cell. The
	// deterministic Bomb.Power field stores arm reach, so normalize once at
	// this transport boundary and keep the engine representation consistent.
	if power <= 1 || power > 0x100 {
		return Event{}, fmt.Errorf("battle runtime bomb player %d has invalid native wire power %d", playerID, power)
	}
	return runtime.engine.ApplyVerifiedBombPlacementAt(playerID, cell, power-1, property, placedAtMS)
}

// AcceptNativePickup mirrors an arbitrator-confirmed pickup for either an
// original participant or a virtual participant. The packet producer is the
// authenticated human arbitrator; PlayerID in the body identifies the actor
// whose scene state changed.
func (runtime *Runtime) AcceptNativePickup(playerID uint16, sceneID uint32, position Position) (Event, error) {
	if runtime == nil || runtime.engine == nil {
		return Event{}, fmt.Errorf("battle runtime is nil")
	}
	if runtime.engine.actorIndex(playerID) < 0 {
		return Event{}, fmt.Errorf("battle runtime pickup player %d is not a participant", playerID)
	}
	return runtime.engine.ApplyVerifiedPickupAt(playerID, sceneID, position)
}

// AcceptNativeFieldObjectContact mirrors an arbitrator-confirmed 0x0FAD whose
// ItemID is the placed banana/slow-glue action (42/43), not a wall pickup.
func (runtime *Runtime) AcceptNativeFieldObjectContact(playerID uint16, actionID uint8, position Position) ([]Event, error) {
	if runtime == nil || runtime.engine == nil {
		return nil, fmt.Errorf("battle runtime is nil")
	}
	return runtime.engine.ApplyVerifiedFieldObjectContact(playerID, actionID, position)
}

// AcceptNativeBombExplosion mirrors the elected original-client arbitrator's
// complete explosion result. The caller has already authenticated and decoded
// 0x0FA4; this bridge updates only the deterministic mirror. destroyedItems is
// the protocol's third vector of visible objects removed by the blast. It must
// never be treated as the hidden contents revealed by mapHits.
func (runtime *Runtime) AcceptNativeBombExplosion(bombs []VerifiedExplodedBomb, mapHits []VerifiedMapElementHit, destroyedItems []Pickup) ([]Event, error) {
	if runtime == nil || runtime.engine == nil {
		return nil, fmt.Errorf("battle runtime is nil")
	}
	for _, bomb := range bombs {
		if runtime.engine.actorIndex(bomb.OwnerID) < 0 {
			return nil, fmt.Errorf("battle runtime explosion owner %d is not a participant", bomb.OwnerID)
		}
	}
	return runtime.engine.ApplyVerifiedBombExplosion(bombs, mapHits, destroyedItems)
}

// AcceptNativePickupDispatch mirrors the complete target vector authored by
// the elected original-client dispatcher in 0x0FAE. Object creation and the
// non-pickable landing phase remain driven by the original scene clock.
func (runtime *Runtime) AcceptNativePickupDispatch(dispatchTime uint32, dispatched []Pickup) error {
	if runtime == nil || runtime.engine == nil {
		return fmt.Errorf("battle runtime is nil")
	}
	return runtime.engine.ApplyVerifiedPickupDispatch(dispatchTime, dispatched)
}

// AcceptNativeItemDestruction mirrors 0x0FBF after the caller authenticates
// the elected original-client arbitrator. It updates only the deterministic
// scene mirror; peer clients have already consumed the native notification.
func (runtime *Runtime) AcceptNativeItemDestruction(destroyed []Pickup) error {
	if runtime == nil || runtime.engine == nil {
		return fmt.Errorf("battle runtime is nil")
	}
	return runtime.engine.ApplyVerifiedItemDestruction(destroyed)
}

// AcceptNativeElimination mirrors an arbitrator-confirmed death for either an
// original or virtual participant. This is the reconciliation counterpart of
// EventActorEliminationRequested in mixed live matches.
func (runtime *Runtime) AcceptNativeElimination(playerID, killerID uint16, drops []Pickup) ([]Event, error) {
	if runtime == nil || runtime.engine == nil {
		return nil, fmt.Errorf("battle runtime is nil")
	}
	if runtime.engine.actorIndex(playerID) < 0 {
		return nil, fmt.Errorf("battle runtime elimination player %d is not a participant", playerID)
	}
	return runtime.engine.ApplyVerifiedElimination(playerID, killerID, drops)
}

// AcceptHumanDeparture removes a disconnected or voluntarily departed native
// actor from the live mirror without inventing a death-drop scatter. The room
// and match layers have already projected the native leave notification; this
// boundary exists only so virtual policies stop observing and targeting a
// player who no longer owns a client scene.
func (runtime *Runtime) AcceptHumanDeparture(playerID uint16) error {
	if runtime == nil || runtime.engine == nil {
		return fmt.Errorf("battle runtime is nil")
	}
	index := runtime.engine.actorIndex(playerID)
	if index < 0 || runtime.engine.actors[index].Source != ParticipantHuman {
		return fmt.Errorf("battle runtime departure player %d is not human", playerID)
	}
	if runtime.engine.actors[index].State == ActorEliminated {
		return nil
	}
	// A non-nil empty slice means the native leave supplied no dropped world
	// objects. Passing nil would invoke the offline simulated death scatter.
	_, err := runtime.engine.ApplyVerifiedElimination(playerID, 0, []Pickup{})
	return err
}

// AcceptNativeMapElementMovement mirrors one arbitrator-confirmed ordinary-rule
// map-element push. The outer packet producer is the human arbitrator, while
// PlayerID in the body may identify either a human or a virtual participant.
func (runtime *Runtime) AcceptNativeMapElementMovement(playerID uint16, elementID uint32, source Cell, direction Direction) (Event, error) {
	if runtime == nil || runtime.engine == nil {
		return Event{}, fmt.Errorf("battle runtime is nil")
	}
	if runtime.engine.actorIndex(playerID) < 0 {
		return Event{}, fmt.Errorf("battle runtime map-element player %d is not a participant", playerID)
	}
	return runtime.engine.ApplyVerifiedMapElementMovement(playerID, elementID, source, direction)
}

// AcceptNativeBattleAction mirrors one centre-server-confirmed world-action
// use. PlayerID in 0x0FB0 may identify either a human or virtual participant;
// the reliable server dispatcher remains the sole authority for consumption.
func (runtime *Runtime) AcceptNativeBattleAction(playerID uint16, actionID uint8, position Position, targetBombID uint32) ([]Event, error) {
	if runtime == nil || runtime.engine == nil {
		return nil, fmt.Errorf("battle runtime is nil")
	}
	if runtime.engine.actorIndex(playerID) < 0 {
		return nil, fmt.Errorf("battle runtime action player %d is not a participant", playerID)
	}
	return runtime.engine.ApplyVerifiedBattleAction(playerID, actionID, position, targetBombID)
}

// AcceptNativeBombMovement mirrors one authenticated 0x139C kick result. The
// body actor may be virtual; the authenticated outer producer remains native.
func (runtime *Runtime) AcceptNativeBombMovement(playerID uint16, bombID uint32, source, target Cell) (Event, error) {
	if runtime == nil || runtime.engine == nil {
		return Event{}, fmt.Errorf("battle runtime is nil")
	}
	if runtime.engine.actorIndex(playerID) < 0 {
		return Event{}, fmt.Errorf("battle runtime bomb-movement player %d is not a participant", playerID)
	}
	return runtime.engine.ApplyVerifiedBombMovement(playerID, bombID, source, target)
}

// AcceptNativeTransformationRecovery consumes the elected native
// arbitrator's 0x10E0. Human positions are authoritative checkpoints; virtual
// positions stay server-owned and only their avatar state is reconciled.
func (runtime *Runtime) AcceptNativeTransformationRecovery(playerID uint16, position Position) (Event, bool, error) {
	if runtime == nil || runtime.engine == nil {
		return Event{}, false, fmt.Errorf("battle runtime is nil")
	}
	index := runtime.engine.actorIndex(playerID)
	if index < 0 {
		return Event{}, false, fmt.Errorf("battle runtime avatar-recovery player %d is not a participant", playerID)
	}
	if runtime.engine.actors[index].Source != ParticipantHuman {
		position = Position{}
	}
	return runtime.engine.ApplyVerifiedTransformationRecovery(playerID, position)
}

// AcceptNativeActorHitFrom commits the authority's reliable 0x0FA5 audit,
// augmented with the causal owner remembered from the virtual actor's Type-2
// 0x0FA5 request. The echoed body does not repeat that owner. Position is the
// exact server-authored hit checkpoint returned by the authority, so the
// virtual mirror freezes at the same coordinate shown by clients.
func (runtime *Runtime) AcceptNativeActorHitFrom(playerID, sourceID uint16, position Position, isAvatar bool) (Event, bool, error) {
	if runtime == nil || runtime.engine == nil {
		return Event{}, false, fmt.Errorf("battle runtime is nil")
	}
	return runtime.engine.applyVerifiedActorHit(playerID, sourceID, position, isAvatar, true)
}

func (runtime *Runtime) AcceptNativeRescue(sourceID, targetID uint16, position Position) (Event, bool, error) {
	if runtime == nil || runtime.engine == nil {
		return Event{}, false, fmt.Errorf("battle runtime is nil")
	}
	return runtime.engine.ApplyVerifiedRescue(sourceID, targetID, position)
}

// StepWithTrace accepts human actions only, obtains one action from every
// virtual policy in ascending PlayerID order, validates those choices against
// the engine's legal mask, then performs one atomic engine transition. The
// returned actions are the exact post-guard inputs consumed by Engine.Step.
func (runtime *Runtime) StepWithTrace(humanActions []Action) (RuntimeStepResult, error) {
	return runtime.stepWithTrace(humanActions, false)
}

// StepWithTraceConcurrentPolicies preserves StepWithTrace's deterministic
// transition and ascending action order while evaluating independent virtual
// policies concurrently. The caller must bind a distinct mutable policy
// wrapper to every virtual participant; immutable inference weights may be
// shared. Live multi-bot rooms use this boundary so seven visibility-isolated
// tactical searches do not serialize on one CPU core. Offline callers retain
// StepWithTrace and its more permissive policy-sharing contract.
func (runtime *Runtime) StepWithTraceConcurrentPolicies(humanActions []Action) (RuntimeStepResult, error) {
	return runtime.stepWithTrace(humanActions, true)
}

type runtimePolicyEvaluation struct {
	playerID    uint16
	observation Observation
	legal       []Action
	policyLegal []Action
	snapshot    *Engine
	chosen      Action
	err         error
}

func (runtime *Runtime) stepWithTrace(humanActions []Action, concurrentPolicies bool) (RuntimeStepResult, error) {
	if runtime == nil || runtime.engine == nil {
		return RuntimeStepResult{}, fmt.Errorf("battle runtime is nil")
	}
	actions := append([]Action(nil), humanActions...)
	seenHumans := make(map[uint16]struct{}, len(humanActions))
	for _, action := range humanActions {
		actorIndex := runtime.engine.actorIndex(action.PlayerID)
		if actorIndex < 0 {
			return RuntimeStepResult{}, fmt.Errorf("battle runtime human action player %d is not a participant", action.PlayerID)
		}
		if runtime.engine.actors[actorIndex].Source != ParticipantHuman {
			return RuntimeStepResult{}, fmt.Errorf("battle runtime received external action for virtual player %d", action.PlayerID)
		}
		if _, _, ok := action.Move.delta(); !ok {
			return RuntimeStepResult{}, fmt.Errorf("battle runtime human action player %d has invalid direction %d", action.PlayerID, action.Move)
		}
		if _, duplicate := seenHumans[action.PlayerID]; duplicate {
			return RuntimeStepResult{}, fmt.Errorf("battle runtime repeats human action player %d", action.PlayerID)
		}
		seenHumans[action.PlayerID] = struct{}{}
	}
	if runtime.engine.outcome.Ended {
		events, err := runtime.engine.Step(actions)
		return RuntimeStepResult{Actions: append([]Action(nil), actions...), Events: events}, err
	}
	evaluations := make([]runtimePolicyEvaluation, 0, len(runtime.virtualPlayerIDs))
	for _, playerID := range runtime.virtualPlayerIDs {
		if _, suspended := runtime.suspendedVirtual[playerID]; suspended {
			actions = append(actions, Action{PlayerID: playerID, Move: DirectionNone})
			continue
		}
		observation, err := runtime.engine.Observation(playerID)
		if err != nil {
			return RuntimeStepResult{}, fmt.Errorf("observe virtual player %d: %w", playerID, err)
		}
		legal, err := runtime.engine.LegalActions(playerID)
		if err != nil {
			return RuntimeStepResult{}, fmt.Errorf("legal actions for virtual player %d: %w", playerID, err)
		}
		evaluation := runtimePolicyEvaluation{
			playerID: playerID, observation: observation,
			legal: append([]Action(nil), legal...), policyLegal: append([]Action(nil), legal...),
		}
		if _, ok := runtime.policies[playerID].(SnapshotPolicy); ok {
			evaluation.snapshot, err = runtime.engine.PolicySnapshot(playerID)
			if err != nil {
				return RuntimeStepResult{}, fmt.Errorf("snapshot virtual player %d: %w", playerID, err)
			}
		}
		evaluations = append(evaluations, evaluation)
	}
	evaluate := func(evaluation *runtimePolicyEvaluation) {
		policy := runtime.policies[evaluation.playerID]
		if snapshotPolicy, ok := policy.(SnapshotPolicy); ok {
			evaluation.chosen, evaluation.err = snapshotPolicy.ChooseActionWithSnapshot(
				evaluation.snapshot, evaluation.observation, evaluation.policyLegal,
			)
			return
		}
		evaluation.chosen, evaluation.err = policy.ChooseAction(evaluation.observation, evaluation.policyLegal)
	}
	if concurrentPolicies && len(evaluations) > 1 {
		var wait sync.WaitGroup
		wait.Add(len(evaluations))
		for index := range evaluations {
			evaluation := &evaluations[index]
			go func() {
				defer wait.Done()
				evaluate(evaluation)
			}()
		}
		wait.Wait()
	} else {
		for index := range evaluations {
			evaluate(&evaluations[index])
		}
	}
	for _, evaluation := range evaluations {
		if evaluation.err != nil {
			return RuntimeStepResult{}, fmt.Errorf("choose action for virtual player %d: %w", evaluation.playerID, evaluation.err)
		}
		if evaluation.chosen.PlayerID != evaluation.playerID {
			return RuntimeStepResult{}, fmt.Errorf("virtual policy %d returned action for player %d", evaluation.playerID, evaluation.chosen.PlayerID)
		}
		if !containsAction(evaluation.legal, evaluation.chosen) {
			return RuntimeStepResult{}, fmt.Errorf("virtual policy %d returned illegal action %+v", evaluation.playerID, evaluation.chosen)
		}
		actions = append(actions, evaluation.chosen)
	}
	actions = runtime.suppressMarginallyUnsafeVirtualBombs(actions)
	events, err := runtime.engine.Step(actions)
	return RuntimeStepResult{Actions: append([]Action(nil), actions...), Events: events}, err
}

// Step preserves the compact engine/offline-training API. Live adapters that
// need native held-input semantics use StepWithTrace instead.
func (runtime *Runtime) Step(humanActions []Action) ([]Event, error) {
	result, err := runtime.StepWithTrace(humanActions)
	return result.Events, err
}

// suppressMarginallyUnsafeVirtualBombs closes the one gap that independent
// per-actor policy search cannot observe: several virtual actors may place
// bubbles in the same atomic tick. Each proposal can be safe in isolation but
// their combined chain may make every placer inescapably self-trap. This guard
// advances only disposable engine clones and removes a placement pulse only
// when the joint proposal traps that placer without winning and the same held
// movement survives when the implicated placement pulse is absent.
func (runtime *Runtime) suppressMarginallyUnsafeVirtualBombs(actions []Action) []Action {
	if runtime == nil || runtime.engine == nil || runtime.engine.outcome.Ended {
		return actions
	}
	virtualBombIndexes := make([]int, 0)
	for index := range actions {
		if !actions[index].PlaceBomb {
			continue
		}
		actorIndex := runtime.engine.actorIndex(actions[index].PlayerID)
		if actorIndex >= 0 && runtime.engine.actors[actorIndex].Source == ParticipantVirtualAI {
			virtualBombIndexes = append(virtualBombIndexes, index)
		}
	}
	if len(virtualBombIndexes) == 0 {
		return actions
	}
	result := append([]Action(nil), actions...)
	for {
		unsafe := runtime.unsafeVirtualBombPlacers(result, virtualBombIndexes)
		if len(unsafe) == 0 {
			return result
		}
		changed := false
		for _, actionIndex := range unsafe {
			without := append([]Action(nil), result...)
			without[actionIndex].PlaceBomb = false
			if runtime.virtualActorSurvivesJointPlan(without, result[actionIndex].PlayerID) {
				result[actionIndex].PlaceBomb = false
				changed = true
			}
		}
		if changed {
			continue
		}
		// Circular chain: A's bubble makes B unsafe and B's makes A unsafe, so
		// removing either one in isolation is insufficient. Remove the complete
		// unsafe set only if doing so restores every implicated actor.
		withoutUnsafe := append([]Action(nil), result...)
		for _, actionIndex := range unsafe {
			withoutUnsafe[actionIndex].PlaceBomb = false
		}
		allRecover := true
		for _, actionIndex := range unsafe {
			if !runtime.virtualActorSurvivesJointPlan(withoutUnsafe, result[actionIndex].PlayerID) {
				allRecover = false
				break
			}
		}
		if !allRecover {
			return result
		}
		for _, actionIndex := range unsafe {
			result[actionIndex].PlaceBomb = false
		}
		return result
	}
}

func (runtime *Runtime) unsafeVirtualBombPlacers(actions []Action, candidateIndexes []int) []int {
	unsafe := make([]int, 0, len(candidateIndexes))
	for _, actionIndex := range candidateIndexes {
		if actionIndex < 0 || actionIndex >= len(actions) || !actions[actionIndex].PlaceBomb {
			continue
		}
		if !runtime.virtualActorSurvivesJointPlan(actions, actions[actionIndex].PlayerID) {
			unsafe = append(unsafe, actionIndex)
		}
	}
	return unsafe
}

func (runtime *Runtime) virtualActorSurvivesJointPlan(actions []Action, playerID uint16) bool {
	clone := runtime.engine.Clone()
	clone.rules.NativeOutcomeAuthority = false
	remaining := saturatingAdd(clone.rules.BombFuseMS, clone.rules.FlameDurationMS)
	first := true
	escape := tacticalEscapeRollout{}
	for remaining > 0 && !clone.outcome.Ended {
		pulses := append([]Action(nil), actions...)
		if !first {
			for index := range pulses {
				pulses[index].PlaceBomb = false
				pulses[index].UseActionID = 0
			}
			for index := range pulses {
				if pulses[index].PlayerID == playerID {
					pulses[index].Move = escape.movement(clone, playerID, remaining, pulses[index].Move)
					break
				}
			}
		}
		if _, err := clone.Step(pulses); err != nil {
			return false
		}
		if remaining <= clone.rules.TickMS {
			break
		}
		remaining -= clone.rules.TickMS
		first = false
	}
	actorIndex := clone.actorIndex(playerID)
	if actorIndex < 0 {
		return false
	}
	actor := clone.actors[actorIndex]
	if clone.outcome.Ended && !clone.outcome.Draw && clone.outcome.WinnerTeamID == actor.TeamID {
		return true
	}
	return actor.State == ActorActive
}

func containsAction(actions []Action, target Action) bool {
	for _, action := range actions {
		if action == target {
			return true
		}
	}
	return false
}
