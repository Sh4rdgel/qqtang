package battleengine

import (
	"reflect"
	"testing"

	"qqtang/internal/game/mapdata"
)

func TestScheduledSupplyUsesNativeTimeAndRecordedEmpty(t *testing.T) {
	items := []mapdata.CompetitiveWallItem{{SceneID: 2, Quantity: 12}}
	a, err := ScheduledPickupsFromItems(0x12345678, items)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := ScheduledPickupsFromItems(0x12345678, items)
	if !reflect.DeepEqual(a, b) || len(a) != 12 {
		t.Fatal("native schedule not reproducible")
	}
	for _, item := range a {
		if item.DueMS < 60_000 || item.DueMS > 150_000 || item.DueMS%10_000 != 0 {
			t.Fatalf("bad due time %+v", item)
		}
	}
	if _, err := ScheduledPickupsFromItems(1, []mapdata.CompetitiveWallItem{{SceneID: 20044, Quantity: 1}}); err == nil {
		t.Fatal("inventory ID accepted as scene item")
	}
	empty, err := ScheduledPickupsFromItems(1, nil)
	if err != nil || len(empty) != 0 {
		t.Fatal("empty historical Items invented supply")
	}
}

func TestScheduledSupplyDispatchesOnceAndHidesFuture(t *testing.T) {
	config := testConfig()
	config.ScheduledPickups = []NativeScheduledPickup{{SceneID: 2, DueMS: 60_000}}
	e := mustEngine(t, config)
	e.elapsedMS = 59_999
	if got := e.dispatchRecycledPickups(); len(got) != 0 {
		t.Fatal("supply arrived before due time")
	}
	clone := e.Clone()
	clone.scheduledPickups[0].SceneID = 1
	if e.scheduledPickups[0].SceneID != 2 {
		t.Fatal("clone shares scheduled state")
	}
	policy, err := e.PolicySnapshot(config.Participants[0].PlayerID)
	if err != nil || len(policy.scheduledPickups) != 0 {
		t.Fatal("policy can inspect future supply")
	}
	e.elapsedMS = 60_000
	if got := e.dispatchRecycledPickups(); len(got) != 1 || len(e.scheduledPickups) != 0 || len(e.pendingPickupDispatches) != 1 {
		t.Fatalf("dispatch failed: events=%v pending=%v", got, e.pendingPickupDispatches)
	}
	e.elapsedMS = 90_000
	if got := e.dispatchRecycledPickups(); len(got) != 0 {
		t.Fatal("finite supply repeated")
	}
	config.ScheduledPickups = nil
	if err := e.Reset(config); err != nil || len(e.scheduledPickups) != 0 || len(e.pendingPickupDispatches) != 0 {
		t.Fatal("schedule leaked across reset")
	}
	config.Rules.NativeOutcomeAuthority = true
	config.ScheduledPickups = []NativeScheduledPickup{{SceneID: 2, DueMS: 60_000}}
	e = mustEngine(t, config)
	e.elapsedMS = 90_000
	if got := e.dispatchRecycledPickups(); len(got) != 0 {
		t.Fatal("live mirror generated a duplicate bird")
	}
}

func TestScheduledSupplySharesBatchCapAndRetriesUnsentItems(t *testing.T) {
	config := testConfig()
	config.Grid = testOpenGrid(15, 13)
	config.ScheduledPickups = []NativeScheduledPickup{}
	for n := 0; n < 10; n++ {
		config.ScheduledPickups = append(config.ScheduledPickups, NativeScheduledPickup{SceneID: 2, DueMS: 60_000})
	}
	e := mustEngine(t, config)
	for n := 0; n < 60; n++ {
		e.recycledPickupSceneIDs = append(e.recycledPickupSceneIDs, 1)
	}
	e.elapsedMS = 60_000
	if got := e.dispatchRecycledPickups(); len(got) != 64 || len(e.scheduledPickups) != 0 {
		t.Fatalf("combined native64 clamp/retirement mismatch: %d / %v", len(got), e.scheduledPickups)
	}
	e = mustEngine(t, config)
	for i := range e.grid.Cells {
		e.grid.Cells[i].Kind = CellSolid
	}
	e.elapsedMS = 60_000
	if got := e.dispatchRecycledPickups(); len(got) != 0 || len(e.scheduledPickups) != 10 {
		t.Fatal("unsent delayed items were lost without open cells")
	}
	e.grid = config.Grid.Clone()
	e.elapsedMS = 89_999
	if got := e.dispatchRecycledPickups(); len(got) != 0 {
		t.Fatal("shared30s gate ignored")
	}
	e.elapsedMS = 90_000
	if got := e.dispatchRecycledPickups(); len(got) != 10 {
		t.Fatalf("unsent items did not retry: %d", len(got))
	}
	// Only seven of ten equal-ID/equal-time records fit this first packet.
	// Native retirement must consume seven copies, not every matching copy.
	e = mustEngine(t, config)
	for i := range e.grid.Cells {
		e.grid.Cells[i].Kind = CellSolid
	}
	for col := int16(1); col <= 7; col++ {
		index := int(5)*int(e.grid.Width) + int(col)
		e.grid.Cells[index].Kind = CellOpen
	}
	e.elapsedMS = 60_000
	if got := e.dispatchRecycledPickups(); len(got) != 7 || len(e.scheduledPickups) != 3 {
		t.Fatalf("equal-key retirement lost unsent copies: sent%d remaining%d", len(got), len(e.scheduledPickups))
	}
}
