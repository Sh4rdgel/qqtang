package battleengine

import "testing"

func TestSecondLayerWallExitKeepsCollisionAndLegalDirections(t *testing.T) {
	for _, scene := range []uint32{0, 110} {
		cfg := testConfig()
		cfg.Grid = testOpenGrid(15, 13)
		cfg.Rules.TickMS = 20
		cfg.Rules.ActorHalfSizePixels = NativeActorHalfSizePixels
		cfg.Rules.SpeedPixelsPerSecondByRate = nativeSpeedPixelsPerSecondByRate
		cfg.Participants[0].Source = ParticipantVirtualAI
		cfg.Grid.Cells[6*15+13] = Tile{Kind: CellSolid, MapElementOccupied: true, NativeGridAttrSet: true,
			NativePlayerExitAttr: 15, NativePlayerExitAttrSet: true}
		e := mustEngine(t, cfg)
		a := &e.actors[0]
		a.Position = Position{558, 255}
		a.SpeedPixelsPerSecond = 180
		if scene != 0 {
			a.TransformationSceneID = scene
			a.AvatarRoleID = 42
		}
		if !e.canProduceNativeMovement(*a, DirectionRight) {
			t.Fatal("native open source-layer exit was masked")
		}
		if e.canProduceNativeMovement(*a, DirectionLeft) {
			t.Fatal("wall interior became freely traversable")
		}
		if _, moved := e.moveActor(0, DirectionRight); !moved || a.Position.X <= 558 {
			t.Fatal("legal wall exit did not execute")
		}
		a.Position = Position{580, 255}
		if e.canProduceNativeMovement(*a, DirectionLeft) {
			t.Fatal("normal movement can enter static wall without native pass")
		}
	}
}

func TestFirstLayerDirectionalExitStillBlocksEvenWithOpenDestination(t *testing.T) {
	cfg := testConfig()
	cfg.Grid = testOpenGrid(5, 5)
	cfg.Participants[0].Spawn = Cell{2, 2}
	cfg.Participants[1].Spawn = Cell{4, 4}
	cfg.Grid.Cells[12] = Tile{Kind: CellOpen, NativeGridAttr: 15, NativeGridAttrSet: true,
		NativePlayerExitAttr: 7196, NativePlayerExitAttrSet: true}
	e := mustEngine(t, cfg)
	e.actors[0].Position = Position{118, 100}
	if e.canProduceNativeMovement(e.actors[0], DirectionRight) {
		t.Fatal("first-layer reverse direction restriction lost")
	}
}
