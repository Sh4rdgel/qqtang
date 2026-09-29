package battleenv

import (
	"os"
	"path/filepath"
	"testing"

	"qqtang/internal/game/battleengine"
	"qqtang/internal/game/mapdata"
)

func TestKungfu03TrainingPlanesKeepPigBedsSolidAndNotPushable(t *testing.T) {
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
		t.Fatal("Kungfu03 missing")
	}
	grid, err := battleengine.GridFromCompetitiveMap(entry)
	if err != nil {
		t.Fatal(err)
	}
	ob := battleengine.Observation{PlayerID: 1, Grid: grid,
		Actors: []battleengine.ActorObservation{{PlayerID: 1, TeamID: 1, Cell: battleengine.Cell{Row: 11, Col: 4}}}}
	tensors := newTensorBatch(1, 1, int(grid.Height), int(grid.Width))
	if err := encodeActor(&tensors, 0, 0, ob, battleengine.DangerTimeline{}, battleengine.TacticalConsequences{}, battleengine.ActionMask{}); err != nil {
		t.Fatal(err)
	}
	for _, anchor := range []battleengine.Cell{
		{Row: 2, Col: 4}, {Row: 2, Col: 9}, {Row: 6, Col: 0},
		{Row: 6, Col: 13}, {Row: 10, Col: 4}, {Row: 10, Col: 9},
	} {
		for dc := 0; dc < 2; dc++ {
			index := int(anchor.Row)*int(grid.Width) + int(anchor.Col) + dc
			at := func(channel int) float32 { return tensors.Spatial[channel*len(grid.Cells)+index] }
			if at(1) != 0 || at(3) != 1 || at(5) != 0 || at(6) != 0 || at(terrainOccupiedChannel) != 1 {
				t.Fatalf("pig bed %d encoded as floor or pushable terrain", index)
			}
		}
	}
}
