package battleengine

import (
	"reflect"
	"testing"
)

func TestPolicyFactsIgnoreUnobservedAllocationAndPrivateEnemyState(t *testing.T) {
	config := testConfig()
	cell := Cell{Row: 0, Col: 2}
	config.Grid.Cells[2] = Tile{Kind: CellBreakable, Durability: 1}
	config.Pickups = []Pickup{{SceneID: SceneBombCapacitySmall, Cell: cell, State: PickupHidden}}
	engine := mustEngine(t, config)
	read := func() (Observation, DangerTimeline, TacticalConsequences) {
		observation, err := engine.Observation(1)
		if err != nil {
			t.Fatal(err)
		}
		danger, err := engine.DangerTimeline(3500)
		if err != nil {
			t.Fatal(err)
		}
		facts, err := engine.TacticalConsequencesForPlayer(1, danger)
		if err != nil {
			t.Fatal(err)
		}
		return observation, danger, facts
	}
	before, timeline, facts := read()
	legalBefore := mustLegalActions(t, engine, 1)
	engine.pickups[0].SceneID = SceneBombPowerSmall
	engine.actors[1].HiddenPickupReachExpiresAt = 99999
	engine.actors[1].SceneFourEffectExpiresAt = 88888
	engine.actors[1].OxygenValue = 1234
	engine.actors[1].MatchSugar = 4567
	after, timelineAfter, factsAfter := read()
	if !reflect.DeepEqual(legalBefore, mustLegalActions(t, engine, 1)) {
		t.Fatal("hidden state leaked into legal action mask")
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("unobserved state leaked into observation")
	}
	if !reflect.DeepEqual(timeline, timelineAfter) {
		t.Fatal("hidden item allocation leaked into danger projection")
	}
	if !reflect.DeepEqual(facts, factsAfter) {
		t.Fatal("private enemy state leaked into tactical facts")
	}
	// Observations own their storage; user inference must not mutate the world.
	after.Grid.Cells[0] = Tile{Kind: CellSolid}
	after.Actors[0].BombPower = 9
	unchanged, _, _ := read()
	if !reflect.DeepEqual(before, unchanged) {
		t.Fatal("observation alias mutated authoritative state")
	}
	engine.actors[0].HiddenPickupReachExpiresAt = 99999
	visible, _, _ := read()
	if len(visible.Pickups) != 1 || visible.Pickups[0].SceneID != SceneBombPowerSmall {
		t.Fatal("local detector must still reveal the actual nearby hidden item")
	}
}
