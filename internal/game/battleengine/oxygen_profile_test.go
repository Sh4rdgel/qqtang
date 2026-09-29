package battleengine

import (
	"testing"

	"qqtang/internal/game/mapdata"
)

func TestPublicWallProfileExcludesDisabledOxygen(t *testing.T) {
	entry := mapdata.CompetitiveMap{NativeRule: 1, HiddenItemCells: make([]mapdata.CompetitiveCell, 10), WallItemRules: []mapdata.CompetitiveWallItemRule{
		{SceneID: SceneOxygenBottle, Minimum: 1, Maximum: 1, Probability: 1},
		{SceneID: SceneOxygenValueAdd, Minimum: 1, Maximum: 1, Probability: 1},
	}}
	profile := publicWallItemProfile(entry)
	if got := profile.Categories[WallItemUtility]; got.PresenceProbability != 0 || got.ExpectedDensity != 0 {
		t.Fatalf("disabled oxygen advertised in observation: %+v", got)
	}
	entry.WallItemRules = append(entry.WallItemRules, mapdata.CompetitiveWallItemRule{SceneID: SceneFourBubbleEffect, Minimum: 1, Maximum: 1, Probability: 1})
	profile = publicWallItemProfile(entry)
	if got := profile.Categories[WallItemUtility]; got.PresenceProbability != 1 || got.ExpectedDensity != .1 {
		t.Fatalf("unrelated utility distribution changed: %+v", got)
	}
}
