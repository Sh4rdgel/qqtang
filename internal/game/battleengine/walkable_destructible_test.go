package battleengine

import "testing"

func grassTile(cell Cell) Tile {
	return Tile{Kind: CellOpen, FlamePassable: true, MapElementOccupied: true,
		NativeGridAttr: 7967, NativeGridAttrSet: true,
		Durability: 1, MapElementID: 6003, ElementWidth: 1, ElementHeight: 1, ElementAnchor: cell}
}

func TestWalkableGrassBurnsAndClearsPlacementWhileFlameContinues(t *testing.T) {
	config := testConfig()
	cell := Cell{Row: 1, Col: 2}
	config.Grid.Cells[7] = grassTile(cell)
	engine := mustEngine(t, config)
	if !engine.canProduceNativeMovement(engine.actors[0], DirectionRight) {
		t.Fatal("grass blocks movement")
	}
	engine.actors[0].Position = PositionAtCellCenter(cell)
	if _, placed := engine.placeBomb(0); placed {
		t.Fatal("grass allowed placement before removal")
	}
	engine.actors[0].Position = PositionAtCellCenter(Cell{Row: 0, Col: 0})
	engine.bombs = []Bomb{{ID: 1, OwnerID: 1, Cell: Cell{Row: 1, Col: 1}, Power: 3, ExplodeAtMS: 100}}
	forecast, err := engine.DangerTimeline(1000)
	if err != nil {
		t.Fatal(err)
	}
	if _, hit := forecast.ImpactAt(Cell{Row: 1, Col: 4}); !hit {
		t.Fatal("forecast incorrectly stopped at transmitting grass")
	}
	engine.elapsedMS = 100
	events, err := engine.Step(nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := engine.grid.Cell(cell); got.MapElementOccupied || got.Durability != 0 || got.Kind != CellOpen {
		t.Fatalf("grass survived: %+v", got)
	}
	if !hasEvent(events, EventCellDestroyed, 0) || !hasFlame(engine.flames, Cell{Row: 1, Col: 4}) {
		t.Fatalf("grass damage or ray continuation missing: %+v", events)
	}
	for _, event := range events {
		if event.Kind == EventCellDestroyed && event.Cell == cell && event.OpenedTraversal {
			t.Fatal("already walkable grass was labeled a new walking entrance")
		}
	}
	engine.actors[0].Position = PositionAtCellCenter(cell)
	if _, placed := engine.placeBomb(0); !placed {
		t.Fatal("cleared grass cell still blocks placement")
	}
}

func TestVerifiedGrassDestructionClearsLiveMirror(t *testing.T) {
	config := testConfig()
	cell := Cell{Row: 1, Col: 2}
	config.Grid.Cells[7] = grassTile(cell)
	engine := mustEngine(t, config)
	event, cells, changed, err := engine.applyVerifiedMapElementHit(VerifiedMapElementHit{Cell: cell, MapElementID: 6003})
	if err != nil || !changed || len(cells) != 1 || event.Kind != EventCellDestroyed {
		t.Fatalf("native grass destruction ignored: %+v %v %v %v", event, cells, changed, err)
	}
	if _, _, changed, err := engine.applyVerifiedMapElementHit(VerifiedMapElementHit{Cell: cell, MapElementID: 6003}); err != nil || changed {
		t.Fatalf("duplicate removal not idempotent: %v %v", changed, err)
	}
}
