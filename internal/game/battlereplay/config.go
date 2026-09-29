package battlereplay

import (
	"fmt"

	"qqtang/internal/game/battleengine"
	"qqtang/internal/game/mapdata"
	"qqtang/internal/game/qbv"
	"qqtang/internal/protocol/game"
)

// ConfigOptions contains only replay choices that are not serialized in a
// QBV. TickMS remains caller-owned because the engine is deliberately
// independent from render FPS. SpawnMode may be supplied from external room
// evidence; when nil, it must be uniquely proven by the recording's earliest
// absolute movement checkpoint for every participant.
type ConfigOptions struct {
	TickMS         uint32
	TrapDurationMS uint32
	SpawnMode      *battleengine.NativeSpawnMode
}

// Projection is the complete, source-traceable initial state used by a
// differential replay. Begin and Map are retained so reports can name the
// exact native inputs rather than only the resulting engine state.
type Projection struct {
	Config    battleengine.Config
	Begin     game.GameBeginData
	Map       mapdata.CompetitiveMap
	SpawnMode battleengine.NativeSpawnMode
}

// ConfigFromRecording projects an original-client QBV bootstrap into the
// restricted rule-1 engine. Historical GAME_BEGIN values are authoritative:
// the installed map must match its ID and hash, player order/roles/teams and
// both seeds are preserved, and NewItems is never rerolled through the current
// server wall-item policy.
func ConfigFromRecording(catalog *mapdata.Catalog, recording qbv.Recording, options ConfigOptions) (Projection, error) {
	if catalog == nil {
		return Projection{}, fmt.Errorf("battle replay map catalog is nil")
	}
	begin, err := qbv.ParseBootstrapGameBegin(recording)
	if err != nil {
		return Projection{}, err
	}
	entry, ok := catalog.CompetitiveMapMetadata(begin.MapID)
	if !ok {
		return Projection{}, fmt.Errorf("QBV GAME_BEGIN map %d is not installed in the client catalog", begin.MapID)
	}
	if begin.MapHash != entry.MapHash {
		return Projection{}, fmt.Errorf("QBV GAME_BEGIN map %d hash %q does not match installed hash %q", begin.MapID, begin.MapHash, entry.MapHash)
	}
	participants := make([]battleengine.Participant, len(begin.Players))
	for index, player := range begin.Players {
		if len(player.NewItems) != 0 {
			return Projection{}, fmt.Errorf("QBV GAME_BEGIN player %d has %d carried NewItems types, which the restricted replay engine does not model", player.PlayerID, len(player.NewItems))
		}
		participant, participantErr := battleengine.ParticipantFromNativeRole(
			player.PlayerID,
			uint16(player.RoleID),
			player.TeamID,
			battleengine.ParticipantHuman,
		)
		if participantErr != nil {
			return Projection{}, fmt.Errorf("QBV GAME_BEGIN player %d: %w", player.PlayerID, participantErr)
		}
		participants[index] = participant
	}

	timeline, err := BuildTimeline(recording)
	if err != nil {
		return Projection{}, err
	}
	spawnMode, err := resolveSpawnMode(entry, begin.SpawnSeed, participants, timeline.Checkpoints, options.SpawnMode)
	if err != nil {
		return Projection{}, fmt.Errorf("QBV GAME_BEGIN map %d spawn mode: %w", begin.MapID, err)
	}
	recordedWallItems := make([]mapdata.CompetitiveWallItem, len(begin.NewItems))
	for index, item := range begin.NewItems {
		recordedWallItems[index] = mapdata.CompetitiveWallItem{SceneID: item.ItemID, Quantity: item.Quantity}
	}
	recordedDelayedItems := make([]mapdata.CompetitiveWallItem, len(begin.Items))
	for index, item := range begin.Items {
		recordedDelayedItems[index] = mapdata.CompetitiveWallItem{SceneID: item.ItemID, Quantity: item.Quantity}
	}
	config, err := battleengine.ConfigFromCompetitiveMap(entry, battleengine.CompetitiveMapConfigOptions{
		SpawnSeed: begin.SpawnSeed,
		ItemSeed:  begin.ItemSeed,
		SpawnMode: spawnMode,
		Rules: battleengine.Rules{
			TickMS:         options.TickMS,
			TrapDurationMS: options.TrapDurationMS,
		},
		Participants:         participants,
		RecordedWallItems:    recordedWallItems,
		RecordedDelayedItems: recordedDelayedItems,
		UseRecordedWallItems: true,
	})
	if err != nil {
		return Projection{}, fmt.Errorf("project QBV GAME_BEGIN map %d: %w", begin.MapID, err)
	}
	return Projection{Config: config, Begin: begin, Map: entry, SpawnMode: spawnMode}, nil
}

func resolveSpawnMode(
	entry mapdata.CompetitiveMap,
	spawnSeed uint32,
	participants []battleengine.Participant,
	checkpoints []ActorCheckpoint,
	explicit *battleengine.NativeSpawnMode,
) (battleengine.NativeSpawnMode, error) {
	if explicit != nil {
		switch *explicit {
		case battleengine.NativeSpawnFree, battleengine.NativeSpawnTeams:
			return *explicit, nil
		default:
			return 0, fmt.Errorf("explicit mode %d is invalid", *explicit)
		}
	}

	earliest := make(map[uint16]ActorCheckpoint, len(participants))
	for _, checkpoint := range checkpoints {
		if _, exists := earliest[checkpoint.PlayerID]; !exists {
			earliest[checkpoint.PlayerID] = checkpoint
		}
	}
	for _, participant := range participants {
		if _, ok := earliest[participant.PlayerID]; !ok {
			return 0, fmt.Errorf("cannot infer mode because player %d has no absolute movement checkpoint", participant.PlayerID)
		}
	}

	matches := make([]battleengine.NativeSpawnMode, 0, 2)
	for _, mode := range [...]battleengine.NativeSpawnMode{battleengine.NativeSpawnFree, battleengine.NativeSpawnTeams} {
		placed, err := battleengine.PlaceNativeSpawns(
			spawnSeed,
			mode,
			competitiveCells(entry.SpawnGroupA),
			competitiveCells(entry.SpawnGroupB),
			participants,
		)
		if err != nil {
			continue
		}
		matched := true
		for _, participant := range placed {
			if earliest[participant.PlayerID].Cell != participant.Spawn {
				matched = false
				break
			}
		}
		if matched {
			matches = append(matches, mode)
		}
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	if len(matches) == 0 {
		return 0, fmt.Errorf("neither native placement algorithm matches every player's earliest checkpoint")
	}
	return 0, fmt.Errorf("both native placement algorithms match the earliest checkpoints; provide explicit room evidence")
}

func competitiveCells(cells []mapdata.CompetitiveCell) []battleengine.Cell {
	result := make([]battleengine.Cell, len(cells))
	for index, cell := range cells {
		result[index] = battleengine.Cell{Row: int16(cell.Row), Col: int16(cell.Col)}
	}
	return result
}
