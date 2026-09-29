package battleengine

import "testing"

func TestBlastBatchUsesOldWallAndPreservesItsNewDrop(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		config := testConfig()
		config.Grid = testOpenGrid(5, 5)
		wall := Cell{Row: 2, Col: 2}
		config.Grid.Cells[12] = Tile{Kind: CellBreakable, Durability: 1}
		config.Pickups = []Pickup{{SceneID: SceneBombCapacitySmall, Cell: wall, State: PickupHidden}}
		engine := mustEngine(t, config)
		engine.elapsedMS = 100
		engine.bombs = []Bomb{
			{ID: 1, OwnerID: 1, Cell: Cell{Row: 2, Col: 0}, Power: 2, ExplodeAtMS: 100},
			{ID: 2, OwnerID: 2, Cell: Cell{Row: 0, Col: 2}, Power: 4, ExplodeAtMS: 100},
		}
		if reverse {
			engine.bombs[0], engine.bombs[1] = engine.bombs[1], engine.bombs[0]
		}
		engine.explodeDueBombs()
		if hasFlame(engine.flames, Cell{Row: 3, Col: 2}) || engine.pickups[0].State != PickupAvailable {
			t.Fatalf("same batch crossed wall or destroyed new drop: reverse=%t flames=%+v pickups=%+v", reverse, engine.flames, engine.pickups)
		}
		// A genuinely later explosion does see the opened wall and old drop.
		engine.elapsedMS = 200
		engine.bombs = []Bomb{{ID: 3, OwnerID: 2, Cell: Cell{Row: 0, Col: 2}, Power: 4, ExplodeAtMS: 200}}
		engine.explodeDueBombs()
		if !hasFlame(engine.flames, Cell{Row: 3, Col: 2}) || engine.pickups[0].State != PickupCollected {
			t.Fatal("later batch did not consume the opened world")
		}
	}
}

func TestPendingNativeHitStopsForcedPhysicsAndContact(t *testing.T) {
	config := testConfig()
	config.Participants[1].Source = ParticipantVirtualAI
	engine := mustEngine(t, config)
	runtime, err := NewRuntime(engine, map[uint16]Policy{2: PolicyFunc(func(Observation, []Action) (Action, error) { t.Fatal("suspended policy invoked"); return Action{}, nil })})
	if err != nil {
		t.Fatal(err)
	}
	actor := &engine.actors[1]
	actor.MovementStatus, actor.MovementStatusExpiresAt = MovementStatusForcedSlide, 9000
	actor.Position = PositionAtCellCenter(Cell{Row: 1, Col: 2})
	actor.nativePreviousPosition = PositionAtCellCenter(Cell{Row: 1, Col: 3})
	engine.pickups = []Pickup{{SceneID: SceneSpeedSmall, Cell: actor.Position.Cell(), State: PickupAvailable}}
	engine.actors[0].Position = actor.Position
	engine.actors[0].State, engine.actors[0].TrapExpiresAt = ActorTrapped, 9000
	if err := runtime.SuspendVirtualActor(2, true); err != nil {
		t.Fatal(err)
	}
	origin := actor.Position
	for i := 0; i < 3; i++ {
		if _, err := runtime.Step(nil); err != nil {
			t.Fatal(err)
		}
	}
	if actor.Position != origin || engine.pickups[0].State != PickupAvailable || engine.actors[0].State != ActorTrapped {
		t.Fatal("pending hit moved or interacted")
	}
}

func TestVerifiedPandaMoveSurvivesFormExpiry(t *testing.T) {
	config := testConfig()
	source := Cell{Row: 0, Col: 1}
	config.Grid.Cells[1] = Tile{Kind: CellBreakable, Durability: 1, MapElementID: 9011, PandaPushable: true, ElementWidth: 1, ElementHeight: 1, ElementAnchor: source}
	engine := mustEngine(t, config) // actor already in ordinary form
	if _, err := engine.ApplyVerifiedMapElementMovement(1, 9011, source, DirectionRight); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.ApplyVerifiedMapElementMovement(1, 9011, Cell{Row: 0, Col: 2}, DirectionNone); err == nil {
		t.Fatal("accepted zero-direction map movement")
	}
}

func TestBirdLandingDependsOnDistanceAndAuthorityRetiresPending(t *testing.T) {
	bird := nativePickupBirdPath{startY: 200}
	near := nativePickupDispatchDelayMS(Cell{Row: 5, Col: 7}, 20, &bird)
	far := nativePickupDispatchDelayMS(Cell{Row: 1, Col: 7}, 20, &bird)
	if near != 2420 || far != 2820 {
		t.Fatalf("landing near=%d far=%d", near, far)
	}
	config := testConfig()
	engine := mustEngine(t, config)
	cell := config.Participants[0].Spawn
	if err := engine.ApplyVerifiedPickupDispatch(0, []Pickup{{SceneID: SceneSpeedSmall, Cell: cell}}); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.ApplyVerifiedPickupAt(1, SceneSpeedSmall, PositionAtCellCenter(cell)); err != nil {
		t.Fatal(err)
	}
	engine.elapsedMS = 9000
	engine.activatePendingPickupDispatches()
	if len(engine.pendingPickupDispatches) != 0 {
		t.Fatal("authority pickup resurrects pending bird drop")
	}
	for _, pickup := range engine.pickups {
		if pickup.State == PickupAvailable {
			t.Fatal("consumed bird drop reappeared")
		}
	}
}

func TestHumanMovementPathOrderingCornerAndStop(t *testing.T) {
	config := testConfig()
	config.Grid = testOpenGrid(8, 5)
	config.Rules.TickMS = 20
	engine := mustEngine(t, config)
	runtime, err := NewRuntime(engine, nil)
	if err != nil {
		t.Fatal(err)
	}
	move := NativeHumanMovement{Sequence: 65535, Current: Position{X: 60, Y: 60}, Corner: Position{X: 80, Y: 60}, End: Position{X: 200, Y: 100}, Direction: DirectionRight, Speed: 1, Moving: true}
	if err := runtime.ReconcileHumanMovementPath(1, move); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 50; i++ {
		if _, err := runtime.Step(nil); err != nil {
			t.Fatal(err)
		}
	}
	if nativeHumanPathPosition(move, 100_000) != move.End {
		t.Fatal("movement continued beyond advertised collision endpoint")
	}
	if engine.actors[0].Position != (Position{X: 80, Y: 80}) {
		t.Fatalf("corner path=%+v", engine.actors[0].Position)
	}
	stop := NativeHumanMovement{Sequence: 0, TimeMS: 1000, Current: Position{X: 80, Y: 80}, End: Position{X: 80, Y: 80}, Direction: DirectionRight, Speed: 1}
	if err := runtime.ReconcileHumanMovementPath(1, stop); err != nil {
		t.Fatal(err)
	}
	if err := runtime.ReconcileHumanMovementPath(1, move); err != nil {
		t.Fatal(err)
	} // old unique checkpoint
	for i := 0; i < 5; i++ {
		runtime.Step(nil)
	}
	if engine.actors[0].Position != stop.Current {
		t.Fatal("stale checkpoint rolled position back or restarted movement")
	}
	stop.Forced = true
	stop.Current.X = 85
	stop.End = stop.Current
	if err := runtime.ReconcileHumanMovementPath(1, stop); err != nil {
		t.Fatal(err)
	}
	if engine.actors[0].Position != stop.Current {
		t.Fatal("same-clock forced correction rejected")
	}
	clone := engine.Clone()
	clone.actors[0].nativeHumanMotion.current.Current.X = 10
	if engine.actors[0].nativeHumanMotion.current.Current.X == 10 {
		t.Fatal("clone shares movement history")
	}
}
