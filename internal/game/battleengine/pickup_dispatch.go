package battleengine

const (
	nativePickupDispatchIntervalMS uint32 = 30_000
	nativePickupBirdStartX         int32  = 800
	nativePickupBirdSpeedXPerMS    int32  = 1 // represented as 1/5 protocol-clock pixel per ms
	nativePickupBirdSpeedDivisor   int32  = 5
	nativePickupDispatchColumns           = 15
	// Live FAE omits bird RNG. This is the user-tuned observation guard, not
	// a recovered physical landing time (300 -> 200 ms, 2026-09-27).
	nativePickupVisibilityDelayMS uint32 = 200
)

// pendingPickupDispatch hides an offline state-3 object until landing, or a
// live FAE target until its observation guard. Native confirmations remain
// authoritative: FAE contains targets, not the bird's random trajectory.
type pendingPickupDispatch struct {
	Pickup       Pickup
	ActivateAtMS uint32
}

type nativePickupBirdPath struct {
	startY int32
	slope  float32
}

func (engine *Engine) newPickupBirdPath() nativePickupBirdPath {
	// 005e24ef ordinary-rule distribution; one trajectory for the whole batch.
	return nativePickupBirdPath{
		startY: 200 + int32(engine.nextSimulationRandom()%200),
		slope:  float32(engine.nextSimulationRandom()%250)*0.001 - 0.125,
	}
}

func nativePickupDispatchDelayMS(cell Cell, tickMS uint32, bird *nativePickupBirdPath) uint32 {
	if tickMS == 0 {
		tickMS = 20
	}
	// Enter column c only AFTER x=(c+1)*40. Quantize to the simulation frame;
	// the real client's render-frame phase is not present in FAE.
	distance := nativePickupBirdStartX - int32(cell.Col+1)*CellSizePixels
	if distance < 0 {
		distance = 0
	}
	flightMS := uint32(distance * nativePickupBirdSpeedDivisor / nativePickupBirdSpeedXPerMS)
	if bird == nil {
		// Preserve the empirical live guard rather than inventing an unsent
		// trajectory or imposing a worst-case delay on every visible item.
		// Pickup grants still require the native authority's confirmation.
		return saturatingAdd(flightMS, nativePickupVisibilityDelayMS)
	}
	crossMS := flightMS + 1
	crossMS = (crossMS + tickMS - 1) / tickMS * tickMS
	travelled := float32(crossMS) * 0.2
	landingMS := func(path nativePickupBirdPath) uint32 {
		sourceRow := int32(float32(path.startY)+travelled*path.slope) / CellSizePixels
		rows := sourceRow - int32(cell.Row)
		if rows < 0 {
			rows = -rows
		}
		// 005d9bbf(...,400,0) means 400 pixels/s, not 400 milliseconds.
		duration := uint32(rows*CellSizePixels) * 1000 / 400
		return (duration + tickMS - 1) / tickMS * tickMS
	}
	return saturatingAdd(crossMS, landingMS(*bird))
}

func (engine *Engine) schedulePickupDispatch(dispatchTime uint32, pickup Pickup, bird *nativePickupBirdPath) {
	engine.retirePendingPickupDispatchesAtCell(pickup.Cell)
	engine.retirePickupsAtCell(pickup.Cell)
	engine.retireFieldObjectsAtCell(pickup.Cell)
	pickup.State = PickupAvailable
	engine.pendingPickupDispatches = append(engine.pendingPickupDispatches, pendingPickupDispatch{
		Pickup: pickup, ActivateAtMS: saturatingAdd(dispatchTime, nativePickupDispatchDelayMS(pickup.Cell, engine.rules.TickMS, bird)),
	})
}

func (engine *Engine) activatePendingPickupDispatches() {
	if engine == nil || len(engine.pendingPickupDispatches) == 0 {
		return
	}
	kept := engine.pendingPickupDispatches[:0]
	for _, pending := range engine.pendingPickupDispatches {
		if pending.ActivateAtMS > engine.elapsedMS {
			kept = append(kept, pending)
			continue
		}
		if nativeFieldSceneID(pending.Pickup.SceneID) {
			engine.installAuthoritativeFieldObject(uint8(pending.Pickup.SceneID), pending.Pickup.Cell)
			continue
		}
		engine.retireFieldObjectsAtCell(pending.Pickup.Cell)
		engine.installAuthoritativePickup(pending.Pickup)
	}
	engine.pendingPickupDispatches = kept
}

func (engine *Engine) retirePendingPickupDispatchesAtCell(cell Cell) {
	if engine == nil || len(engine.pendingPickupDispatches) == 0 {
		return
	}
	kept := engine.pendingPickupDispatches[:0]
	for _, pending := range engine.pendingPickupDispatches {
		if pending.Pickup.Cell != cell {
			kept = append(kept, pending)
		}
	}
	engine.pendingPickupDispatches = kept
}

// destroyBlastObjectsAtCell mirrors the ordinary-rule portion of the native
// 0x0FBF producer. An already visible collectible is removed and queued for
// the client's bird/dispatcher. A pickup revealed from the wall by this same
// blast is intentionally processed later and therefore survives this flame.
// Native movement fields 42/43 also enter this queue, preserving their scene
// IDs (not converting them back to inventory pickups 23/25). Each stacked
// object is one entry. FAE carries no owner, so the landed field is neutral.
func (engine *Engine) destroyBlastObjectsAtCell(cell Cell, ownerID uint16, bombID uint32) []Event {
	if engine == nil {
		return nil
	}
	events := []Event{}
	for index := range engine.pickups {
		pickup := &engine.pickups[index]
		if pickup.Cell != cell || pickup.State != PickupAvailable {
			continue
		}
		pickup.State = PickupCollected
		if !engine.rules.NativeOutcomeAuthority {
			engine.recycledPickupSceneIDs = append(engine.recycledPickupSceneIDs, pickup.SceneID)
		}
		events = append(events, Event{
			Kind: EventPickupDestroyed, TimeMS: engine.elapsedMS, PlayerID: ownerID,
			BombID: bombID, Cell: cell, SceneID: pickup.SceneID,
		})
	}
	for _, object := range engine.fieldObjects {
		if object.Cell != cell || (object.ActionID != 42 && object.ActionID != 43) {
			continue
		}
		if !engine.rules.NativeOutcomeAuthority {
			engine.recycledPickupSceneIDs = append(engine.recycledPickupSceneIDs, uint32(object.ActionID))
		}
		events = append(events, Event{
			Kind: EventPickupDestroyed, TimeMS: engine.elapsedMS, PlayerID: ownerID,
			BombID: bombID, Cell: cell, SceneID: uint32(object.ActionID), ObjectID: object.ID,
		})
	}
	engine.retireFieldObjectsAtCell(cell)
	return events
}

// dispatchRecycledPickups mirrors 005e280c/0060e288 for rule1. Destroyed items
// and mature GAME_BEGIN.Items share the 30s gate and one bird. Live authority
// mirrors never produce their own dispatch, avoiding duplicate client supply.
func (engine *Engine) dispatchRecycledPickups() []Event {
	if engine == nil || engine.rules.NativeOutcomeAuthority {
		return nil
	}
	if engine.elapsedMS-engine.lastPickupDispatchMS < nativePickupDispatchIntervalMS {
		return nil
	}
	mature := make([]NativeScheduledPickup, 0, len(engine.scheduledPickups))
	for _, item := range engine.scheduledPickups {
		if item.DueMS <= engine.elapsedMS {
			mature = append(mature, item)
		}
	}
	if len(engine.recycledPickupSceneIDs) == 0 && len(mature) == 0 {
		return nil
	}
	engine.lastPickupDispatchMS = engine.elapsedMS
	// Select for the full queues, then serialize <=64 immediate and <=32
	// delayed entries. 005e24b6 further clamps their combined bird list to64.
	immediate := len(engine.recycledPickupSceneIDs)
	cells := engine.nativeWholeMapDropCells(immediate + len(mature))
	ordinaryCount := min(immediate, len(cells), NativePickupDispatchMaximum)
	delayedCount := min(max(0, len(cells)-immediate), len(mature), 32)
	dispatched := make([]uint32, 0, ordinaryCount+delayedCount)
	dispatched = append(dispatched, engine.recycledPickupSceneIDs[:ordinaryCount]...)
	for _, item := range mature[:delayedCount] {
		dispatched = append(dispatched, item.SceneID)
	}
	if len(dispatched) > NativePickupDispatchMaximum {
		dispatched = dispatched[:NativePickupDispatchMaximum]
	}
	events := make([]Event, 0, len(dispatched))
	bird := engine.newPickupBirdPath()
	for index, sceneID := range dispatched {
		pickup := Pickup{SceneID: sceneID, Cell: cells[index], State: PickupAvailable}
		engine.schedulePickupDispatch(engine.elapsedMS, pickup, &bird)
		events = append(events, Event{
			Kind: EventPickupDispatched, TimeMS: engine.elapsedMS,
			Cell: pickup.Cell, Position: PositionAtCellCenter(pickup.Cell), SceneID: pickup.SceneID,
		})
	}
	// FUN_005e2d0a clears the whole immediate vector after one dispatch attempt,
	// including entries beyond the protocol/cell capacity.
	engine.recycledPickupSceneIDs = nil
	// 005e2771 removes ONE matching ID+time for each serialized entry, by
	// swapping in the last record. Equal-key unsent copies must survive, and
	// the resulting order determines which mature copies are selected next.
	for _, sent := range mature[:delayedCount] {
		for index, item := range engine.scheduledPickups {
			if item == sent {
				last := len(engine.scheduledPickups) - 1
				engine.scheduledPickups[index] = engine.scheduledPickups[last]
				engine.scheduledPickups = engine.scheduledPickups[:last]
				break
			}
		}
	}
	return events
}

func (engine *Engine) nativeWholeMapDropCells(count int) []Cell {
	if count <= 0 {
		return nil
	}
	candidates := make([]Cell, 0, int(engine.grid.Width)*int(engine.grid.Height))
	for row := int16(0); row < int16(engine.grid.Height); row++ {
		for col := int16(0); col < int16(engine.grid.Width); col++ {
			cell := Cell{Row: row, Col: col}
			tile, _ := engine.grid.Cell(cell)
			if tile.Kind != CellOpen || tile.MapElementOccupied || engine.bombAt(cell) >= 0 || engine.deathDropCellOccupied(cell) {
				continue
			}
			candidates = append(candidates, cell)
		}
	}
	for len(candidates) > count {
		remove := int(engine.nextSimulationRandom() % uint64(len(candidates)))
		copy(candidates[remove:], candidates[remove+1:])
		candidates = candidates[:len(candidates)-1]
	}
	return candidates
}
