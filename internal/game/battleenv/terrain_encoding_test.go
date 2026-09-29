package battleenv

import (
	"qqtang/internal/game/battleengine"
	"testing"
)

func TestTerrainPlanesKeepDirectionsAndWalkableDurability(t *testing.T) {
	for _, tc := range []struct {
		attr            uint32
		movement, flame [4]float32
	}{
		{5397, [4]float32{1, 0, 1, 0}, [4]float32{1, 0, 1, 0}},
		{6682, [4]float32{0, 1, 0, 1}, [4]float32{0, 1, 0, 1}},
		{7196, [4]float32{1, 1, 0, 0}, [4]float32{1, 1, 0, 0}},
		{7967, [4]float32{1, 1, 1, 1}, [4]float32{1, 1, 1, 1}},
		{0x0f00, [4]float32{}, [4]float32{1, 1, 1, 1}},
	} {
		tile := battleengine.Tile{Kind: battleengine.CellOpen, NativeGridAttrSet: true, NativeGridAttr: tc.attr, MapElementOccupied: true, Durability: 1}
		ob := battleengine.Observation{PlayerID: 1, Grid: battleengine.Grid{Width: 1, Height: 1, Cells: []battleengine.Tile{tile}}, Actors: []battleengine.ActorObservation{{PlayerID: 1, TeamID: 1}}}
		tensors := newTensorBatch(1, 1, 1, 2) // second cell is padding, not floor
		if err := encodeActor(&tensors, 0, 0, ob, battleengine.DangerTimeline{}, battleengine.TacticalConsequences{}, battleengine.ActionMask{}); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 4; i++ {
			if tensors.Spatial[(playerEntryChannelStart+i)*2] != tc.movement[i] || tensors.Spatial[(flameEntryChannelStart+i)*2] != tc.flame[i] {
				t.Fatalf("attr %#x direction %d encoded incorrectly", tc.attr, i)
			}
		}
		if tensors.Spatial[terrainDurabilityChannel*2] != 0.25 || tensors.Spatial[terrainOccupiedChannel*2] != 1 || tensors.Spatial[2] != 0 || tensors.Spatial[4] != 0 {
			t.Fatal("walkable durability or legacy placement changed")
		}
		for c := playerEntryChannelStart; c < SpatialChannels; c++ {
			if tensors.Spatial[c*2+1] != 0 {
				t.Fatal("terrain leaked into padding")
			}
		}
	}
}
