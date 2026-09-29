package battleengine

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"qqtang/internal/game/mapdata"
)

func TestKungfu03PigBedsRemainOccupiedAfterPushAndBombRequests(t *testing.T) {
	root := os.Getenv("QQT_AI_TEST_CLIENT_ROOT")
	if root == "" {
		root = filepath.Join("..", "..", "..", "runtime", "client-patched")
	}
	if _, err := os.Stat(filepath.Join(root, "map", "pig03_8.map")); os.IsNotExist(err) {
		t.Skip("verified client resources absent")
	}
	catalog, err := mapdata.LoadCatalog(root)
	if err != nil {
		t.Fatal(err)
	}
	entry, ok := catalog.CompetitiveMap(903)
	if !ok {
		t.Fatal("Kungfu03 is missing")
	}
	grid, err := GridFromCompetitiveMap(entry)
	if err != nil {
		t.Fatal(err)
	}
	// The report concerns a cleared battlefield. Remove only breakable crates,
	// retaining the real 2x1 pig beds and their exact native collision cells.
	for i, tile := range grid.Cells {
		if tile.Kind == CellBreakable {
			grid.Cells[i] = Tile{Kind: CellOpen, FlamePassable: true}
		}
	}
	for _, anchor := range []Cell{{2, 4}, {2, 9}, {6, 0}, {6, 13}, {10, 4}, {10, 9}} {
		for dc := int16(0); dc < 2; dc++ {
			cell := Cell{Row: anchor.Row, Col: anchor.Col + dc}
			t.Run(fmt.Sprintf("%d_%d", cell.Row, cell.Col), func(t *testing.T) {
				config := testConfig()
				config.Grid = grid.Clone()
				config.Rules.TickMS = NativeMapElementPushPulseMS
				config.Participants[0].Spawn = Cell{Row: cell.Row + 1, Col: cell.Col}
				config.Participants[1].Spawn = Cell{Row: 0, Col: 14}
				engine := mustEngine(t, config)
				before := engine.grid.Clone()
				for i := 0; i < 40; i++ {
					events, err := engine.Step([]Action{{PlayerID: 1, Move: DirectionUp}})
					if err != nil {
						t.Fatal(err)
					}
					if hasEvent(events, EventMapElementMoved, 0) || hasEvent(events, EventMapElementMoveRequested, 0) {
						t.Fatal("pig bed was treated as a movable crate")
					}
				}
				if engine.actors[0].Position.Cell().Row <= cell.Row || !reflect.DeepEqual(before, engine.grid) {
					t.Fatalf("actor entered or moved the pig bed: %+v", engine.actors[0].Position)
				}
				// Even a native 0xFB3 confirmation is insufficient: the client's
				// map manager refuses dimensions >=2 before changing occupancy.
				if _, err := engine.ApplyVerifiedMapElementMovement(1, 9008, anchor, DirectionUp); err == nil {
					t.Fatal("multi-cell native confirmation changed the world")
				}
				if !reflect.DeepEqual(before, engine.grid) {
					t.Fatal("rejected confirmation mutated collision cells")
				}
				// Placement remains forbidden even if a replay/position update
				// happens to put an actor on the occupied cell.
				engine.actors[0].Position = PositionAtCellCenter(cell)
				legal, err := engine.LegalActions(1)
				if err != nil {
					t.Fatal(err)
				}
				for _, action := range legal {
					if action.PlaceBomb {
						t.Fatal("pig bed exposed a legal bomb action")
					}
				}
				if _, placed := engine.placeBomb(0); placed {
					t.Fatal("bomb placed on pig bed")
				}
			})
		}
	}
}
