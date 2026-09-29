package mapdata

import "testing"

func TestOrdinaryWallSupplyExcludesOxygen(t *testing.T) {
	entry := CompetitiveMap{ID: 905, NativeRule: 1, WallItemRules: []CompetitiveWallItemRule{
		{SceneID: 1, Minimum: 8, Maximum: 8, Probability: 1},
		{SceneID: 26, Minimum: 1, Maximum: 1, Probability: 1},
		{SceneID: 62, Minimum: 1, Maximum: 3, Probability: 1},
		{SceneID: 23, Minimum: 1, Maximum: 3, Probability: .6},
	}}
	// Other native rules remain a reference for the original RNG stream.
	otherRule := entry
	otherRule.NativeRule = 2
	for seed := uint32(1); seed <= 32; seed++ {
		original := otherRule.RollWallItems(seed, 8, 100, 50)
		wantBananas := int16(0)
		for _, item := range original {
			if item.SceneID == 23 {
				wantBananas = item.Quantity
			}
		}
		for _, items := range [][]CompetitiveWallItem{
			entry.RollWallItems(seed, 8, 100, 50),
			entry.RollFinalCompetitiveOrdinaryWallItems(seed, 8, 100, 50),
			entry.RollFinalCompetitiveBossWallItems(seed, 8, 100, 50),
		} {
			for _, item := range items {
				if item.SceneID == 26 || item.SceneID == 62 {
					t.Fatalf("seed %d emitted oxygen: %+v", seed, items)
				}
			}
		}
		gotBananas := int16(0)
		for _, item := range entry.RollWallItems(seed, 8, 100, 50) {
			if item.SceneID == 23 {
				gotBananas = item.Quantity
			}
		}
		if gotBananas != wantBananas {
			t.Fatalf("seed %d unrelated RNG changed: banana %d != %d", seed, gotBananas, wantBananas)
		}
	}
	if len(otherRule.RollWallItems(1, 8, 100, 50)) < 3 {
		t.Fatal("oxygen was removed from an unrelated native rule")
	}
}
