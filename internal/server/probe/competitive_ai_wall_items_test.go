package probe

import (
	"math"
	"slices"
	"testing"

	"qqtang/internal/game/battleengine"
	"qqtang/internal/game/mapdata"
	"qqtang/internal/game/match"
	roomstate "qqtang/internal/game/room"
	"qqtang/internal/protocol/game"
)

func TestCompetitiveGameDataCountsVirtualAIForMultiplayerWallItems(t *testing.T) {
	selected := mapdata.CompetitiveMap{
		ID: 201, NativeRule: 1, Rule: mapdata.CompetitiveRuleOrdinary,
		HiddenItemCells: competitiveTestHiddenCells(80),
	}
	for _, sceneID := range []uint32{1, 2, 3} {
		// A positive probability below any nonzero uint32 RNG fraction makes
		// the participant-dependent floor observable without a lucky high roll.
		selected.WallItemRules = append(selected.WallItemRules, mapdata.CompetitiveWallItemRule{
			SceneID: sceneID, Minimum: 1, Maximum: 20, Probability: math.SmallestNonzeroFloat32,
		})
	}
	server := &Server{}
	session := &connectionSession{CurrentGameID: 9, Profile: game.DefaultPlayerProfile()}
	for _, humanCount := range []int{1, 4, 8} {
		participants := make([]match.CompetitiveParticipant, 8)
		for index := range participants {
			source := match.CompetitiveParticipantHuman
			if index >= humanCount {
				source = match.CompetitiveParticipantVirtualAI
			}
			participants[index] = match.CompetitiveParticipant{
				PlayerID: uint16(index + 1), RoleID: 7, TeamID: byte(index + 1), Source: source,
			}
		}
		data, err := server.newCompetitiveGameData(session, selected, roomstate.CompetitiveFieldNoItem, 1, participants, false)
		if err != nil {
			t.Fatal(err)
		}
		// The eight-role roster uses the current 70% wall budget: 50 basic
		// upgrades plus six maximum-strength upgrades. Human/AI identity
		// must not change that allocation.
		want := []game.GameItemType{
			{ItemID: 1, Quantity: 19}, {ItemID: 2, Quantity: 17}, {ItemID: 3, Quantity: 14},
			{ItemID: 6, Quantity: 2}, {ItemID: 7, Quantity: 2}, {ItemID: 8, Quantity: 2},
		}
		if len(data.Players) != 8 || !slices.Equal(data.NewItems, want) {
			t.Fatalf("%d humans + %d AI: players=%d items=%+v, want %+v", humanCount, 8-humanCount, len(data.Players), data.NewItems, want)
		}
		// Offline games use the shared roster-based supply plan. Live AI
		// consumes GAME_BEGIN; both must place identical hidden pickups.
		roles := make([]uint16, len(participants))
		for index, participant := range participants {
			roles[index] = uint16(participant.RoleID)
		}
		plan := selected.PlanCompetitiveSupply(data.ItemSeed, roles, false)
		training, err := battleengine.HiddenPickupsFromCompetitiveWallItems(selected, data.ItemSeed, plan.WallItems)
		if err != nil {
			t.Fatal(err)
		}
		live, err := battleengine.HiddenPickupsFromCompetitiveWallItems(selected, data.ItemSeed, competitiveAIWallItems(data.NewItems))
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(training, live) {
			t.Fatalf("%d humans: native training and GAME_BEGIN hidden pickups differ", humanCount)
		}
	}
}
