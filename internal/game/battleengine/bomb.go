package battleengine

import "sort"

func (engine *Engine) placeBomb(actorIndex int) (Event, bool) {
	bomb, ok := engine.prospectiveBomb(actorIndex, true)
	if !ok {
		return Event{}, false
	}
	return engine.commitBomb(bomb), true
}

// placeBombOnSnapshotEmptyCell is used by Step after it has evaluated native
// local occupancy against the pre-placement battlefield. FUN_005b08e0 checks
// the local cell before producing 0xFA3, while the remote 0xFA3 consumer at
// FUN_006084bf creates the bubble without repeating that occupancy check.
// Consequently, different players that all pass their local check in one
// input frame may produce multiple bubbles in one cell (the native 叠炮 race).
func (engine *Engine) placeBombOnSnapshotEmptyCell(actorIndex int) (Event, bool) {
	bomb, ok := engine.prospectiveBomb(actorIndex, false)
	if !ok {
		return Event{}, false
	}
	return engine.commitBomb(bomb), true
}

// prospectiveBomb applies the same placement/capacity rules as the native
// producer without mutating engine state. requireEmpty distinguishes ordinary
// sequential placement from Step's already-validated simultaneous stack race.
func (engine *Engine) prospectiveBomb(actorIndex int, requireEmpty bool) (Bomb, bool) {
	actor := &engine.actors[actorIndex]
	cell := actor.Position.Cell()
	if requireEmpty && engine.bombAt(cell) >= 0 {
		return Bomb{}, false
	}
	// The original local producer rejects placement unless the actor's scene
	// cell is an ordinary open grid cell. Native pass/up-wall can temporarily
	// render an actor over scenery, but it does not turn that scenery into a
	// legal bubble cell.
	tile, inside := engine.grid.Cell(cell)
	if !inside || tile.Kind != CellOpen || tile.MapElementOccupied {
		return Bomb{}, false
	}
	capacity := engine.actorCapabilities(actor, actor.Facing).EffectiveBombCapacity
	if engine.activeBombCount(actor.PlayerID) >= int(capacity) {
		return Bomb{}, false
	}
	return Bomb{
		ID: engine.nextBombID, OwnerID: actor.PlayerID, Cell: cell, Power: actor.BombPower,
		// 005df7d0 requires a negative remaining fuse; zero is still armed.
		ExplodeAtMS:     saturatingAdd(saturatingAdd(engine.elapsedMS, engine.rules.BombFuseMS), 1),
		SceneFourEffect: engine.sceneFourEffectActive(actor),
	}, true
}

func (engine *Engine) commitBomb(bomb Bomb) Event {
	engine.nextBombID++
	engine.bombs = append(engine.bombs, bomb)
	return Event{
		Kind: EventBombPlaced, TimeMS: engine.elapsedMS, PlayerID: bomb.OwnerID,
		BombID: bomb.ID, Cell: bomb.Cell,
	}
}

func (engine *Engine) explodeDueBombs() []Event {
	return engine.explodeDueBombsMatching(nil)
}

func (engine *Engine) explodeDueBombsMatching(acceptRoot func(Bomb) bool) []Event {
	queue := make([]uint32, 0)
	var scheduled map[uint32]bool
	for _, bomb := range engine.bombs {
		if bomb.EffectiveExplodeAtMS() <= engine.elapsedMS && (acceptRoot == nil || acceptRoot(bomb)) {
			if scheduled == nil {
				scheduled = make(map[uint32]bool)
			}
			queue = append(queue, bomb.ID)
			scheduled[bomb.ID] = true
		}
	}
	if len(queue) == 0 {
		return []Event{}
	}
	exploded := make(map[uint32]bool)
	events := make([]Event, 0)
	batch := blastBatch{wallSeen: make(map[Cell]bool), objectSeen: make(map[Cell]bool)}
	for len(queue) > 0 {
		bombID := queue[0]
		queue = queue[1:]
		bombIndex := engine.bombIndexByID(bombID)
		if bombIndex < 0 || exploded[bombID] {
			continue
		}
		bomb := engine.bombs[bombIndex]
		exploded[bombID] = true
		blastCells := engine.blastCells(bomb, scheduled, &queue, &batch)
		rowMin, rowMax := bomb.Cell.Row, bomb.Cell.Row
		colMin, colMax := bomb.Cell.Col, bomb.Cell.Col
		for _, cell := range blastCells {
			if cell.Row < rowMin {
				rowMin = cell.Row
			}
			if cell.Row > rowMax {
				rowMax = cell.Row
			}
			if cell.Col < colMin {
				colMin = cell.Col
			}
			if cell.Col > colMax {
				colMax = cell.Col
			}
		}
		events = append(events, Event{
			Kind: EventBombExploded, TimeMS: engine.elapsedMS, PlayerID: bomb.OwnerID,
			BombID: bomb.ID, TriggeredByBombID: bomb.TriggeredByBombID, Cell: bomb.Cell,
			BlastRowMin: rowMin, BlastRowMax: rowMax, BlastColMin: colMin, BlastColMax: colMax,
		})
		for _, cell := range blastCells {
			engine.addBombFlame(cell, bomb.OwnerID, bomb.ID)
		}
	}
	// Native FA4 computes the entire due/chain batch against the old static
	// layers. Remove old visible objects before revealing this batch's drops.
	events = append(events, engine.commitBlastBatch(batch)...)
	if len(exploded) == 0 {
		return events
	}
	keptBombs := engine.bombs[:0]
	for _, bomb := range engine.bombs {
		if !exploded[bomb.ID] {
			keptBombs = append(keptBombs, bomb)
		}
	}
	engine.bombs = keptBombs
	return events
}

func (engine *Engine) bombIndexByID(bombID uint32) int {
	for index := range engine.bombs {
		if engine.bombs[index].ID == bombID {
			return index
		}
	}
	return -1
}

type blastCellHit struct {
	cell Cell
	bomb Bomb
}

type blastBatch struct {
	walls, objects       []blastCellHit
	wallSeen, objectSeen map[Cell]bool
}

func (batch *blastBatch) hit(cell Cell, bomb Bomb, wall bool) {
	seen, hits := batch.objectSeen, &batch.objects
	if wall {
		seen, hits = batch.wallSeen, &batch.walls
	}
	if !seen[cell] {
		seen[cell] = true
		*hits = append(*hits, blastCellHit{cell: cell, bomb: bomb})
	}
}

func (engine *Engine) commitBlastBatch(batch blastBatch) []Event {
	var events []Event
	for _, hit := range batch.objects {
		events = append(events, engine.destroyBlastObjectsAtCell(hit.cell, hit.bomb.OwnerID, hit.bomb.ID)...)
	}
	for _, hit := range batch.walls {
		cell, bomb := hit.cell, hit.bomb
		index := engine.gridIndex(cell)
		tile := engine.grid.Cells[index]
		if tile.Durability > 1 {
			engine.grid.Cells[index].Durability--
			events = append(events, Event{Kind: EventCellDamaged, TimeMS: engine.elapsedMS, PlayerID: bomb.OwnerID, BombID: bomb.ID, Cell: cell, ObjectID: tile.MapElementID})
		} else {
			engine.grid.Cells[index] = Tile{Kind: CellOpen, FlamePassable: true}
			events = append(events, Event{Kind: EventCellDestroyed, TimeMS: engine.elapsedMS, PlayerID: bomb.OwnerID, BombID: bomb.ID, Cell: cell, ObjectID: tile.MapElementID,
				OpenedTraversal: !tile.PlayerPassable(DirectionUp) || !tile.PlayerPassable(DirectionRight) || !tile.PlayerPassable(DirectionDown) || !tile.PlayerPassable(DirectionLeft)})
			if event, revealed := engine.revealPickupAt(cell, bomb.OwnerID, bomb.ID); revealed {
				events = append(events, event)
			}
		}
	}
	return events
}

func (engine *Engine) blastCells(bomb Bomb, scheduled map[uint32]bool, queue *[]uint32, batch *blastBatch) []Cell {
	result := []Cell{bomb.Cell}
	batch.hit(bomb.Cell, bomb, false)
	directions := [...]Cell{{Row: -1}, {Col: 1}, {Row: 1}, {Col: -1}}
	for directionIndex, direction := range directions {
		worldDirection := Direction(directionIndex) + DirectionUp
		previous, _ := engine.grid.Cell(bomb.Cell)
		for distance := int16(1); distance <= int16(bomb.Power); distance++ {
			if !previous.FlamePassableIn(oppositeDirection(worldDirection)) {
				break
			}
			cell := Cell{Row: bomb.Cell.Row + direction.Row*distance, Col: bomb.Cell.Col + direction.Col*distance}
			tile, ok := engine.grid.Cell(cell)
			if !ok {
				break
			}
			if tile.Durability > 0 {
				batch.hit(cell, bomb, true)
				// Walkable grass may transmit this ray while being destroyed.
				// Blocking breakable walls retain the native stop-on-hit rule.
				if !tile.FlamePassableIn(worldDirection) {
					result = append(result, cell)
					break
				}
			}
			if !tile.FlamePassableIn(worldDirection) {
				break
			}
			previous = tile
			result = append(result, cell)
			batch.hit(cell, bomb, false)
			otherBomb := false
			for otherIndex := range engine.bombs {
				otherBombObject := &engine.bombs[otherIndex]
				otherID := otherBombObject.ID
				if otherBombObject.Cell != cell || otherID == bomb.ID {
					continue
				}
				otherBomb = true
				// Native flame contact reduces the remaining fuse to an immediate
				// value even during flight, but state 3 still blocks explosion until
				// landing. Preserve that two-clock behavior instead of exploding a
				// thrown bomb in mid-air.
				if otherBombObject.FlightUntilMS > engine.elapsedMS {
					if otherBombObject.ExplodeAtMS > engine.elapsedMS {
						otherBombObject.ExplodeAtMS = engine.elapsedMS
						if otherBombObject.TriggeredByBombID == 0 {
							otherBombObject.TriggeredByBombID = bomb.ID
						}
					}
					continue
				}
				if !scheduled[otherID] {
					scheduled[otherID] = true
					if otherBombObject.TriggeredByBombID == 0 {
						otherBombObject.TriggeredByBombID = bomb.ID
					}
					*queue = append(*queue, otherID)
				}
			}
			if otherBomb {
				break
			}
		}
	}
	return result
}

func (engine *Engine) addFlame(cell Cell, ownerID uint16) {
	engine.addBombFlame(cell, ownerID, 0)
}

func (engine *Engine) addBombFlame(cell Cell, ownerID uint16, bombID uint32) {
	expires := saturatingAdd(engine.elapsedMS, engine.rules.FlameDurationMS)
	// The list is kept in cell order. Insert one cell instead of sorting the
	// complete list after every arm pixel in every speculative explosion.
	index := sort.Search(len(engine.flames), func(i int) bool {
		other := engine.flames[i].Cell
		return other.Row > cell.Row || (other.Row == cell.Row && other.Col >= cell.Col)
	})
	if index < len(engine.flames) && engine.flames[index].Cell == cell {
		engine.flames[index].ImpactAtMS = engine.elapsedMS
		if engine.flames[index].ExpiresAtMS < expires {
			engine.flames[index].ExpiresAtMS = expires
		}
		engine.flames[index].OwnerID = ownerID
		engine.flames[index].BombID = bombID
		return
	}
	engine.flames = append(engine.flames, Flame{})
	copy(engine.flames[index+1:], engine.flames[index:len(engine.flames)-1])
	engine.flames[index] = Flame{Cell: cell, OwnerID: ownerID, BombID: bombID, ImpactAtMS: engine.elapsedMS, ExpiresAtMS: expires}
}

func (engine *Engine) expireFlames() {
	kept := engine.flames[:0]
	for _, flame := range engine.flames {
		if flame.ExpiresAtMS > engine.elapsedMS {
			kept = append(kept, flame)
		}
	}
	engine.flames = kept
}
