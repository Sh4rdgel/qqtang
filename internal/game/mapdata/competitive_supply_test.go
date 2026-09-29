package mapdata

import (
	"reflect"
	"testing"
)

func TestCompetitiveSupplyProtectsOtherItemsAndBossFork(t *testing.T) {
	entry := CompetitiveMap{ID: 905, NativeRule: 1, Rule: CompetitiveRuleOrdinary,
		HiddenItemCells: make([]CompetitiveCell, 65)}
	entry.WallItemRules = []CompetitiveWallItemRule{
		{SceneID: 1, Minimum: 8, Maximum: 8, Probability: 1},
		{SceneID: 2, Minimum: 8, Maximum: 8, Probability: 1},
		{SceneID: 3, Minimum: 8, Maximum: 8, Probability: 1},
		{SceneID: 25, Minimum: 4, Maximum: 4, Probability: 1},
	}
	for _, boss := range []bool{false, true} {
		roles := []uint16{1, 2, 3, 4, 5, 6, 7, 8}
		p := entry.PlanCompetitiveSupply(1, roles, boss)
		q := map[uint32]int{}
		total := 0
		for _, item := range p.WallItems {
			q[item.SceneID] += int(item.Quantity)
			total += int(item.Quantity)
		}
		if total > 65*7/10 || p.OtherReserved < 17 || q[25] < 3 {
			t.Fatalf("lost capacity/reserve: %+v", p)
		}
		floor := 2
		if boss {
			floor = 4
			if q[24] < 1 {
				t.Fatal("Boss fork missing")
			}
		}
		for _, id := range []uint32{6, 7, 8} {
			if q[id] < floor {
				t.Fatalf("upper floor stolen: %v", q)
			}
		}
		later := 0
		for _, item := range p.DelayedItems {
			if item.SceneID < 1 || item.SceneID > 3 {
				t.Fatal("delayed supply must contain basic attributes only")
			}
			later += int(item.Quantity)
		}
		if later > CompetitiveDelayedSupplyLimit || !reflect.DeepEqual(p, entry.PlanCompetitiveSupply(1, roles, boss)) {
			t.Fatal("unbounded or nondeterministic supply")
		}
	}
}

func TestCompetitiveDelayedSupplyRuleBoundaries(t *testing.T) {
	for _, rule := range []uint32{1, 3, 4, 5, 6, 7, 8, 13} {
		if !(CompetitiveMap{NativeRule: rule}).HasNativeDelayedItemSupply() {
			t.Fatalf("missing native rule %d", rule)
		}
	}
	for _, entry := range []CompetitiveMap{{NativeRule: 2}, {ID: 1111, NativeRule: 5}, {NativeRule: 12}} {
		if entry.HasNativeDelayedItemSupply() {
			t.Fatalf("unsupported competitive Items path: %+v", entry)
		}
	}
}
