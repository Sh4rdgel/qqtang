package battleengine

import (
	"fmt"

	"qqtang/internal/game/mapdata"
)

// NativeRuntimeParticipant is the complete identity projection needed to
// construct a rule-1 actor. Combat attributes always come from the client's
// RoleID table rather than caller-supplied approximations.
type NativeRuntimeParticipant struct {
	PlayerID uint16
	RoleID   uint16
	TeamID   byte
	Source   ParticipantSource
}

// CompetitiveRuntimeOptions contains the native GAME_BEGIN seeds, fixed-step
// cadence and policy bindings needed by both a live server match and a future
// offline training environment. TrapDurationMS is optional for live use,
// where ConfirmTrappedDeath consumes the client's authoritative Die callback;
// an offline environment can set it to an explicitly chosen training value.
type CompetitiveRuntimeOptions struct {
	SimulationSeed uint64
	SpawnSeed      uint32
	ItemSeed       uint32
	SpawnMode      NativeSpawnMode
	TickMS         uint32
	TrapDurationMS uint32
	// VirtualTrapDurationMS is used only by server-owned AI actors. It must not
	// turn into a guessed timeout for connected human clients.
	VirtualTrapDurationMS uint32
	// NativeOutcomeAuthority makes live virtual actors behave as non-arbitrator
	// peers: actions come from Go, while the elected original client confirms
	// shared hits and interactions. Offline and training callers leave it false.
	NativeOutcomeAuthority bool
	// RecordedWallItems is the exact GAME_BEGIN.NewItems list used by the
	// original clients in a live match. UseRecordedWallItems distinguishes an
	// intentionally empty list from an offline environment that should roll its
	// own quantities from the map profile.
	RecordedWallItems    []mapdata.CompetitiveWallItem
	RecordedDelayedItems []mapdata.CompetitiveWallItem
	UseRecordedWallItems bool
	Participants         []NativeRuntimeParticipant
	Policies             map[uint16]Policy
}

// NewRuntimeFromCompetitiveMap is the one-shot construction boundary for the
// restricted native battle. It projects roles, loads exact map collision,
// spawns and wall items, creates the deterministic engine, then binds virtual
// participants to policies. It does not create network sessions or choose an
// arbitrator; virtual participants remain ineligible through ParticipantSource.
func NewRuntimeFromCompetitiveMap(entry mapdata.CompetitiveMap, options CompetitiveRuntimeOptions) (*Runtime, error) {
	participants := make([]Participant, len(options.Participants))
	for index, native := range options.Participants {
		participant, err := ParticipantFromNativeRole(native.PlayerID, native.RoleID, native.TeamID, native.Source)
		if err != nil {
			return nil, fmt.Errorf("competitive map %d participant %d: %w", entry.ID, index, err)
		}
		participants[index] = participant
	}
	config, err := ConfigFromCompetitiveMap(entry, CompetitiveMapConfigOptions{
		SimulationSeed: options.SimulationSeed,
		SpawnSeed:      options.SpawnSeed,
		ItemSeed:       options.ItemSeed,
		SpawnMode:      options.SpawnMode,
		Rules: Rules{
			TickMS:                 options.TickMS,
			TrapDurationMS:         options.TrapDurationMS,
			VirtualTrapDurationMS:  options.VirtualTrapDurationMS,
			NativeOutcomeAuthority: options.NativeOutcomeAuthority,
		},
		Participants:         participants,
		RecordedWallItems:    append([]mapdata.CompetitiveWallItem(nil), options.RecordedWallItems...),
		RecordedDelayedItems: append([]mapdata.CompetitiveWallItem(nil), options.RecordedDelayedItems...),
		UseRecordedWallItems: options.UseRecordedWallItems,
	})
	if err != nil {
		return nil, err
	}
	engine, err := New(config)
	if err != nil {
		return nil, fmt.Errorf("competitive map %d engine: %w", entry.ID, err)
	}
	runtime, err := NewRuntime(engine, options.Policies)
	if err != nil {
		return nil, fmt.Errorf("competitive map %d runtime: %w", entry.ID, err)
	}
	return runtime, nil
}
