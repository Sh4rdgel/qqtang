package battleengine

import "testing"

func nativeTerrain(attr uint32) Tile {
	kind := CellOpen
	if attr&15 == 0 {
		kind = CellSolid
	}
	return Tile{Kind: kind, FlamePassable: attr&0xf00 != 0, MapElementOccupied: true, NativeGridAttrSet: true, NativeGridAttr: attr}
}

func TestNativeDirectionalTerrainMovementAndForecast(t *testing.T) {
	// Exact catalog masks: water 5010 is vertical; 17108 is horizontal;
	// 17112 is a corner; the actor-only fixture isolates the two mask groups.
	for _, tc := range []struct {
		name        string
		attr        uint32
		move, flame [4]bool
	}{
		{"vertical_5010", 5397, [4]bool{true, false, true, false}, [4]bool{true, false, true, false}},
		{"horizontal_17108", 6682, [4]bool{false, true, false, true}, [4]bool{false, true, false, true}},
		{"corner_17112", 7196, [4]bool{true, true, false, false}, [4]bool{true, true, false, false}},
		{"actor_only", 5, [4]bool{true, false, true, false}, [4]bool{}},
		{"flame_only", 3840, [4]bool{}, [4]bool{true, true, true, true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for i, direction := range []Direction{DirectionUp, DirectionRight, DirectionDown, DirectionLeft} {
				config := testConfig()
				config.Grid = testOpenGrid(7, 7)
				config.Rules.ActorHalfSizePixels = 19
				gate := Cell{Row: 3, Col: 3}
				dx, dy, _ := direction.delta()
				origin := Cell{Row: gate.Row - int16(dy), Col: gate.Col - int16(dx)}
				beyond := Cell{Row: gate.Row + int16(dy), Col: gate.Col + int16(dx)}
				config.Grid.Cells[3*7+3] = nativeTerrain(tc.attr)
				config.Participants[0].Spawn = origin
				config.Participants[1].Spawn = Cell{Row: 6, Col: 6}
				engine := mustEngine(t, config)
				engine.moveActor(0, direction)
				moved := engine.actors[0].Position != PositionAtCellCenter(origin)
				if moved != tc.move[i] {
					t.Fatalf("direction %v movement=%v want %v", direction, moved, tc.move[i])
				}
				engine.actors[0].Position = PositionAtCellCenter(Cell{})
				engine.bombs = []Bomb{{ID: 1, OwnerID: 1, Cell: origin, Power: 3, ExplodeAtMS: 100}}
				timeline, err := engine.DangerTimeline(1000)
				if err != nil {
					t.Fatal(err)
				}
				_, hit := timeline.ImpactAt(gate)
				if hit != tc.flame[i] {
					t.Fatalf("direction %v forecast gate=%v want %v", direction, hit, tc.flame[i])
				}
				_, past := timeline.ImpactAt(beyond)
				wantPast := tc.flame[i] && tc.flame[(i+2)%4]
				if past != wantPast {
					t.Fatalf("direction %v forecast crosses far edge=%v want %v", direction, past, wantPast)
				}
				engine.elapsedMS = 100
				engine.explodeDueBombs()
				if hasFlame(engine.flames, gate) != hit || hasFlame(engine.flames, beyond) != past {
					t.Fatal("forecast diverges from actual explosion")
				}
			}
		})
	}
}

func TestNativeCornerSourceExitUsesReverseDirection(t *testing.T) {
	config := testConfig()
	config.Grid = testOpenGrid(5, 5)
	config.Participants[0].Spawn = Cell{Row: 2, Col: 2}
	config.Participants[1].Spawn = Cell{Row: 4, Col: 4}
	config.Grid.Cells[12] = nativeTerrain(7196) // native entry masks Right+Up
	engine := mustEngine(t, config)
	start := engine.actors[0].Position
	// Starting just inside the right edge makes destination probes completely
	// clear; the closed source exit must still prevent this displacement.
	engine.actors[0].Position.X += 18
	start = engine.actors[0].Position
	if engine.resolveNativeMovementDisplacement(&engine.actors[0], DirectionRight, 5, false) || engine.actors[0].Position != start {
		t.Fatal("destination-only collision bypassed the corner's closed exit")
	}
	// Opposite side is open: reverse query for Left selects the Right mask.
	engine.actors[0].Position = PositionAtCellCenter(Cell{Row: 2, Col: 2})
	engine.actors[0].Position.X -= 18
	if !engine.resolveNativeMovementDisplacement(&engine.actors[0], DirectionLeft, 5, false) {
		t.Fatal("corner's native open exit was blocked")
	}
}

func TestDirectionalTerrainCannotTurnSidewaysInsideGate(t *testing.T) {
	config := testConfig()
	config.Grid = testOpenGrid(5, 5)
	config.Participants[0].Spawn = Cell{Row: 2, Col: 2}
	config.Participants[1].Spawn = Cell{Row: 4, Col: 4}
	config.Grid.Cells[12] = nativeTerrain(5397)
	engine := mustEngine(t, config)
	if engine.canProduceNativeMovement(engine.actors[0], DirectionRight) {
		t.Fatal("vertical gate permits sideways movement from inside")
	}
	if !engine.canProduceNativeMovement(engine.actors[0], DirectionUp) {
		t.Fatal("vertical gate blocks axial exit")
	}
}

func TestRefugeProjectionDoesNotEscapeAcrossBlockedGateAxis(t *testing.T) {
	for _, tc := range []struct {
		attr   uint32
		refuge bool
	}{{5397, false}, {6682, true}} {
		config := testConfig()
		config.Grid = testOpenGrid(7, 3)
		config.Rules.ActorHalfSizePixels = 19
		for i := range config.Grid.Cells {
			config.Grid.Cells[i] = Tile{Kind: CellSolid}
		}
		for col := 1; col <= 5; col++ {
			config.Grid.Cells[7+col] = Tile{Kind: CellOpen, FlamePassable: true}
		}
		config.Grid.Cells[10] = nativeTerrain(tc.attr)
		config.Participants[0].Spawn = Cell{Row: 1, Col: 1}
		config.Participants[1].Spawn = Cell{Row: 1, Col: 5}
		engine := mustEngine(t, config)
		// A known impact covers the left compartment throughout the horizon;
		// the only whole-cell refuge is beyond the directional gate.
		timeline := DangerTimeline{Width: 7, Height: 3, EarliestImpactMS: make([]uint32, 21), LatestImpactMS: make([]uint32, 21), SafeAfterMS: make([]uint32, 21)}
		for i := range timeline.EarliestImpactMS {
			timeline.EarliestImpactMS[i] = NoDangerImpact
			timeline.LatestImpactMS[i] = NoDangerImpact
			timeline.SafeAfterMS[i] = NoDangerImpact
		}
		for _, i := range []int{8, 9, 10} {
			timeline.EarliestImpactMS[i] = 2000
			timeline.LatestImpactMS[i] = 2000
			timeline.SafeAfterMS[i] = 2001
		}
		horizons := [TacticalReachabilityHorizonCount]uint32{2500, 2500, 2500, 2500, 2500}
		got := engine.tacticalSafeReachabilityFromActor(&engine.actors[0], 0, timeline, horizons, nil)
		if got.refugeFound != tc.refuge {
			t.Fatalf("mask %#x projected refuge=%v want %v", tc.attr, got.refugeFound, tc.refuge)
		}
	}
}
