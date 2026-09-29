package battleengine

import "testing"

func TestDestroyedMovementFieldsReturnAsNeutralFields(t *testing.T) {
	for _, action := range []uint8{42, 43} {
		config := testConfig()
		config.Grid = testOpenGrid(7, 3)
		config.Rules.RoundDurationMS = 120000
		config.Participants[0].Spawn = Cell{1, 0}
		config.Participants[1].Spawn = Cell{1, 6}
		e := mustEngine(t, config)
		cell := Cell{1, 3}
		for id := uint32(1); id <= 3; id++ {
			e.fieldObjects = append(e.fieldObjects, FieldObject{ID: id, ActionID: action, OwnerID: 1, Cell: cell})
		}
		e.elapsedMS = 12000
		events := e.destroyBlastObjectsAtCell(cell, 2, 9)
		if len(events) != 3 || len(e.fieldObjects) != 0 || len(e.recycledPickupSceneIDs) != 3 {
			t.Fatalf("stacked fields lost: %+v / %+v", events, e.recycledPickupSceneIDs)
		}
		// A second overlapping blast must not duplicate the removed objects.
		e.destroyBlastObjectsAtCell(cell, 2, 10)
		if len(e.recycledPickupSceneIDs) != 3 {
			t.Fatal("duplicate blast recycled twice")
		}
		clone := e.Clone()
		clone.recycledPickupSceneIDs[0] = 999
		if e.recycledPickupSceneIDs[0] != uint32(action) {
			t.Fatal("clone shares queue")
		}
		e.elapsedMS = 30000
		dispatched := e.dispatchRecycledPickups()
		if len(dispatched) != 3 || len(e.recycledPickupSceneIDs) != 0 {
			t.Fatal("native30s dispatch lost fields")
		}
		for _, ev := range dispatched {
			if ev.SceneID != uint32(action) {
				t.Fatal("field converted to collectible")
			}
		}
		e.elapsedMS = 40000
		e.activatePendingPickupDispatches()
		if len(e.fieldObjects) != 3 {
			t.Fatalf("landed fields=%d", len(e.fieldObjects))
		}
		for _, o := range e.fieldObjects {
			if o.OwnerID != 0 || o.ActionID != action {
				t.Fatal("native ownerless dispatcher gained ownership")
			}
		}
		landed := e.fieldObjects[0].Cell
		e.destroyBlastObjectsAtCell(landed, 1, 11)
		if len(e.recycledPickupSceneIDs) != 1 || e.recycledPickupSceneIDs[0] != uint32(action) {
			t.Fatal("landed field cannot recycle again")
		}
	}
}

func TestLiveMovementFieldClearDoesNotAuthorBirdQueue(t *testing.T) {
	config := testConfig()
	config.Rules.NativeOutcomeAuthority = true
	e := mustEngine(t, config)
	cell := Cell{0, 0}
	e.fieldObjects = []FieldObject{{ID: 1, ActionID: 43, OwnerID: 1, Cell: cell}}
	e.destroyBlastObjectsAtCell(cell, 2, 1)
	if len(e.fieldObjects) != 0 || len(e.recycledPickupSceneIDs) != 0 {
		t.Fatal("live mirror created a second bird producer")
	}
}

func TestConsumedSlowFieldIsNotRecycled(t *testing.T) {
	config := testConfig()
	config.Rules.SpeedPixelsPerSecondByRate = nativeSpeedPixelsPerSecondByRate
	e := mustEngine(t, config)
	cell := e.actors[1].Position.Cell()
	e.fieldObjects = []FieldObject{{ID: 1, ActionID: 43, OwnerID: 1, Cell: cell}}
	e.actors[1].nativePreviousPosition = PositionAtCellCenter(Cell{0, 0})
	e.resolveFieldObjectContacts()
	if e.actors[1].MovementStatus != MovementStatusSlow || len(e.fieldObjects) != 0 {
		t.Fatal("contact did not consume glue")
	}
	e.destroyBlastObjectsAtCell(cell, 1, 2)
	if len(e.recycledPickupSceneIDs) != 0 {
		t.Fatal("consumed glue was resurrected")
	}
}
