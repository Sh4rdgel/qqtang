package battleengine

import (
	"fmt"

	"qqtang/internal/clientdata/sceneelement"
)

const nativeDeathDropMaximum = 64

// nativeSceneForBattleAction inverts the native scene pickup callbacks. Action
// 46 is intentionally absent: it belongs only to the temporary axe-gang avatar
// and is removed with that avatar before death inventory is enumerated.
func nativeSceneForBattleAction(actionID uint8) (uint32, bool) {
	for _, sceneID := range [...]sceneelement.ID{21, 23, 24, 25, 27, 28} {
		definition, ok := sceneelement.NativeBattleActionPickup(sceneID)
		if ok && definition.ActionID == actionID {
			return uint32(sceneID), true
		}
	}
	return 0, false
}

func (engine *Engine) scatterNativeDeathDrops(actor *Actor) []Pickup {
	if engine == nil || actor == nil {
		return nil
	}
	sceneIDs := make([]uint32, 0, nativeDeathDropMaximum)
	for index, delta := range actor.nativeBasicPickupDelta {
		count := int(delta)
		if count < 0 {
			count = 0
		}
		if count > 10 {
			count = 10
		}
		for item := 0; item < count && len(sceneIDs) < nativeDeathDropMaximum; item++ {
			sceneIDs = append(sceneIDs, uint32(index+1))
		}
	}
	for _, slot := range actor.HeldActions {
		sceneID, ok := nativeSceneForBattleAction(slot.ActionID)
		if !ok {
			continue
		}
		for count := uint8(0); count < slot.Count && len(sceneIDs) < nativeDeathDropMaximum; count++ {
			sceneIDs = append(sceneIDs, sceneID)
		}
	}
	if len(sceneIDs) == 0 {
		return nil
	}
	// Ordinary rule 1 passes the default rectangle to native FUN_005b8574,
	// selecting unoccupied cells across the map. The death producer's 5x5
	// rectangle is conditional on rule 12, not on the number of carried items.
	cells := engine.nativeWholeMapDropCells(len(sceneIDs))
	if len(cells) < len(sceneIDs) {
		sceneIDs = sceneIDs[:len(cells)]
	}
	drops := make([]Pickup, len(sceneIDs))
	for index := range sceneIDs {
		drops[index] = Pickup{SceneID: sceneIDs[index], Cell: cells[index], State: PickupAvailable}
	}
	return drops
}

func (engine *Engine) deathDropCellOccupied(cell Cell) bool {
	for _, actor := range engine.actors {
		if actor.State != ActorEliminated && engine.positionOverlapsCell(actor.Position, cell) {
			return true
		}
	}
	for _, pickup := range engine.pickups {
		if pickup.State != PickupCollected && pickup.Cell == cell {
			return true
		}
	}
	for _, object := range engine.fieldObjects {
		if object.Cell == cell {
			return true
		}
	}
	return false
}

func (engine *Engine) nextSimulationRandom() uint64 {
	// SplitMix64 is deterministic, clone-friendly and does not share process
	// global state. The installed client uses its match PRNG at this boundary;
	// SimulationSeed is the server/training equivalent input.
	engine.rngState += 0x9e3779b97f4a7c15
	value := engine.rngState
	value = (value ^ (value >> 30)) * 0xbf58476d1ce4e5b9
	value = (value ^ (value >> 27)) * 0x94d049bb133111eb
	return value ^ (value >> 31)
}

func (engine *Engine) validateVerifiedDeathDrops(drops []Pickup) error {
	if len(drops) > nativeDeathDropMaximum {
		return fmt.Errorf("verified death drop count %d exceeds %d", len(drops), nativeDeathDropMaximum)
	}
	seen := make(map[Cell]struct{}, len(drops))
	for index, drop := range drops {
		if drop.State != PickupAvailable {
			return fmt.Errorf("verified death drop %d has state %d, want available", index, drop.State)
		}
		if _, _, supported := supportedPickupEffect(drop.SceneID); !supported {
			return fmt.Errorf("verified death drop %d uses unsupported scene ID %d", index, drop.SceneID)
		}
		if _, inside := engine.grid.Cell(drop.Cell); !inside {
			return fmt.Errorf("verified death drop %d cell %d,%d is outside the grid", index, drop.Cell.Row, drop.Cell.Col)
		}
		if _, duplicate := seen[drop.Cell]; duplicate {
			return fmt.Errorf("verified death drops repeat cell %d,%d", drop.Cell.Row, drop.Cell.Col)
		}
		seen[drop.Cell] = struct{}{}
	}
	return nil
}

func (engine *Engine) installDeathDrops(playerID uint16, drops []Pickup) []Event {
	events := make([]Event, 0, len(drops))
	for _, drop := range drops {
		// The native death event has already selected unique live cells. Replace
		// any stale mirror object at that coordinate instead of stacking a second
		// pickup that the arbitrator never registered.
		engine.retirePickupsAtCell(drop.Cell)
		engine.installAuthoritativePickup(drop)
		events = append(events, Event{
			Kind: EventPickupDropped, TimeMS: engine.elapsedMS, PlayerID: playerID,
			Cell: drop.Cell, Position: PositionAtCellCenter(drop.Cell), SceneID: drop.SceneID,
		})
	}
	return events
}
