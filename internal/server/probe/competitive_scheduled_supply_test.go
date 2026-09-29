package probe

import (
	"reflect"
	"testing"

	"qqtang/internal/game/battleengine"
	"qqtang/internal/game/mapdata"
	"qqtang/internal/game/match"
	roomstate "qqtang/internal/game/room"
	"qqtang/internal/protocol/game"
)

func TestCompetitiveStartUsesSharedScheduledSupply(t *testing.T) {
	entry := mapdata.CompetitiveMap{ID: 905, NativeRule: 1, Rule: mapdata.CompetitiveRuleOrdinary,
		HiddenItemCells: competitiveTestHiddenCells(65), WallItemRules: []mapdata.CompetitiveWallItemRule{
			{SceneID: 1, Minimum: 8, Maximum: 8, Probability: 1},
			{SceneID: 2, Minimum: 8, Maximum: 8, Probability: 1},
			{SceneID: 3, Minimum: 8, Maximum: 8, Probability: 1},
		}}
	participants := make([]match.CompetitiveParticipant, 8)
	roles := make([]uint16, 8)
	for i := range participants {
		roles[i] = uint16(i + 1)
		participants[i] = match.CompetitiveParticipant{PlayerID: uint16(i + 1), RoleID: byte(i + 1), TeamID: byte(i + 1)}
	}
	data, err := (&Server{}).newCompetitiveGameData(&connectionSession{CurrentGameID: 1}, entry, roomstate.CompetitiveFieldNoItem, 1, participants, true)
	if err != nil {
		t.Fatal(err)
	}
	plan := entry.PlanCompetitiveSupply(data.ItemSeed, roles, true)
	if !reflect.DeepEqual(data.NewItems, mergeCompetitiveSceneItems(plan.WallItems)) ||
		!reflect.DeepEqual(data.Items, mergeCompetitiveSceneItems(plan.DelayedItems)) || len(data.Items) == 0 {
		t.Fatalf("GAME_BEGIN does not carry shared plan: %+v", data)
	}
	if err := data.Validate(); err != nil {
		t.Fatal(err)
	}
	scheduled, err := battleengine.ScheduledPickupsFromItems(data.ItemSeed, competitiveAIWallItems(data.Items))
	if err != nil || len(scheduled) > mapdata.CompetitiveDelayedSupplyLimit || len(scheduled) == 0 {
		t.Fatalf("offline schedule disagrees: %v / %v", scheduled, err)
	}
}

func TestBattleDispatchCombinedCapAfterNativeMapFilter(t *testing.T) {
	d := game.DispatchItemData{}
	for i := 0; i < 64; i++ {
		d.Items = append(d.Items, game.GameItem{ItemID: 1})
	}
	for i := 0; i < 8; i++ {
		d.DelayedItems = append(d.DelayedItems, game.DelayedGameItem{ItemID: 2, DispatchTime: 60_000})
	}
	if got := battlePickupsFromDispatch(d); len(got) != 64 || got[63].SceneID != 1 {
		t.Fatal("merged native bird capacity not respected")
	}
	d.Items[0].ItemID = 101
	if got := battlePickupsFromDispatch(d, 315); len(got) != 64 || got[63].SceneID != 2 {
		t.Fatal("native map filtering must precede combined clamp")
	}
}
