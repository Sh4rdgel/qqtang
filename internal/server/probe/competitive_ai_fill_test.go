package probe

import (
	"encoding/binary"
	"io"
	"path/filepath"
	"testing"

	"qqtang/internal/game/mapdata"
	"qqtang/internal/game/match"
	roomstate "qqtang/internal/game/room"
	"qqtang/internal/protocol/game"
)

func TestCompetitiveAIRoomProjectionUsesFreeUnlockedSeatsAndCompleteIdentity(t *testing.T) {
	snapshot := testCompetitiveAISnapshot(false, 4)
	snapshot.RoomID = 9
	snapshot.Members = []roomstate.Member{
		{PlayerID: 1, RoleID: 7, TeamID: 1, SeatID: 1},
		{PlayerID: 2, RoleID: 8, TeamID: 1, SeatID: 3},
	}
	participants := []match.CompetitiveParticipant{
		{PlayerID: 20_001, RoleID: 9, TeamID: 2, Source: match.CompetitiveParticipantVirtualAI},
		{PlayerID: 20_002, RoleID: 10, TeamID: 2, Source: match.CompetitiveParticipantVirtualAI},
	}
	projections, err := buildCompetitiveAIRoomProjections(snapshot, participants)
	if err != nil {
		t.Fatal(err)
	}
	if len(projections) != 2 || projections[0].SeatID != 2 || projections[1].SeatID != 4 {
		t.Fatalf("AI room seats = %+v, want 2 and 4", projections)
	}
	notification, err := projections[0].enterNotification(snapshot.RoomID)
	if err != nil {
		t.Fatal(err)
	}
	player := notification.Player
	if notification.RoomID != 9 || player.UIN == 0 || player.PlayerID != 20_001 || player.GameInfo.RoleID != 9 || player.TeamID != 2 || player.SeatID != 2 || player.Status != 0 || player.Nickname != "AI1" {
		t.Fatalf("AI enter notification = %+v", notification)
	}
	if _, err = notification.MarshalNetworkBinary(); err != nil {
		t.Fatalf("marshal AI enter notification: %v", err)
	}
}

func TestPlanCompetitiveAIFillStandardKeepsAFullHumanTeam(t *testing.T) {
	entry := testCompetitiveAIMap(4)
	snapshot := testCompetitiveAISnapshot(false, 4)
	humans := []match.CompetitiveParticipant{
		{PlayerID: 1, RoleID: 7, TeamID: 3},
		{PlayerID: 2, RoleID: 8, TeamID: 3},
	}
	plan, err := planCompetitiveAIFill(snapshot, entry, humans, 77)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Participants) != 2 || plan.Participants[0].TeamID == 3 || plan.Participants[1].TeamID != plan.Participants[0].TeamID {
		t.Fatalf("standard fill = %+v", plan.Participants)
	}
	for _, participant := range plan.Participants {
		if participant.Source != match.CompetitiveParticipantVirtualAI || participant.PlayerID < competitiveAIFirstPlayerID || participant.RoleID == 0 {
			t.Fatalf("invalid virtual participant %+v", participant)
		}
	}
}

func TestPlanCompetitiveAIFillStandardFillsBothTeamsToCapacity(t *testing.T) {
	for _, test := range []struct {
		name       string
		openSeats  byte
		mapLimit   byte
		humanCount int
		teamSize   int
	}{
		{"solo eight seats", 8, 8, 1, 4},
		{"two humans eight seats", 8, 8, 2, 4},
		{"three humans eight seats", 8, 8, 3, 4},
		{"closed seats limit match", 6, 8, 1, 3},
		{"map limits match", 8, 4, 1, 2},
		{"odd open capacity", 7, 8, 2, 3},
		{"odd map capacity", 8, 5, 1, 2},
		{"two open seats", 2, 8, 1, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			snapshot := testCompetitiveAISnapshot(false, test.openSeats)
			humans := make([]match.CompetitiveParticipant, test.humanCount)
			for index := range humans {
				humans[index] = match.CompetitiveParticipant{PlayerID: uint16(index + 1), RoleID: 7, TeamID: 3}
			}
			plan, err := planCompetitiveAIFill(snapshot, testCompetitiveAIMap(test.mapLimit), humans, 77)
			if err != nil {
				t.Fatal(err)
			}
			counts := map[byte]int{3: test.humanCount}
			for _, participant := range plan.Participants {
				counts[participant.TeamID]++
				if participant.Source != match.CompetitiveParticipantVirtualAI || participant.PlayerID < competitiveAIFirstPlayerID {
					t.Fatalf("invalid virtual participant: %+v", participant)
				}
			}
			if len(counts) != 2 {
				t.Fatalf("filled teams = %v, want two teams", counts)
			}
			for teamID, count := range counts {
				if count != test.teamSize {
					t.Fatalf("team %d has %d players, want %d; roster=%+v", teamID, count, test.teamSize, plan.Participants)
				}
			}
		})
	}
}

func TestCompetitiveAIStandardCapacityFillHonorsScatteredClosedSeats(t *testing.T) {
	snapshot := testCompetitiveAISnapshot(false, 8)
	snapshot.LockedSeats[1], snapshot.LockedSeats[5] = true, true
	snapshot.Members = []roomstate.Member{
		{PlayerID: 1, RoleID: 7, TeamID: 3, SeatID: 1},
		{PlayerID: 2, RoleID: 8, TeamID: 3, SeatID: 3},
	}
	humans := []match.CompetitiveParticipant{
		{PlayerID: 1, RoleID: 7, TeamID: 3},
		{PlayerID: 2, RoleID: 8, TeamID: 3},
	}
	plan, err := planCompetitiveAIFill(snapshot, testCompetitiveAIMap(8), humans, 77)
	if err != nil {
		t.Fatal(err)
	}
	projections, err := buildCompetitiveAIRoomProjections(snapshot, plan.Participants)
	if err != nil {
		t.Fatal(err)
	}
	if len(projections) != 4 {
		t.Fatalf("projected AI count = %d, want four for 3v3", len(projections))
	}
	for index, seatID := range []byte{4, 5, 7, 8} {
		if projections[index].SeatID != seatID || projections[index].TeamID != plan.Participants[index].TeamID {
			t.Fatalf("AI projection %d = %+v, want open seat %d and planned team", index, projections[index], seatID)
		}
	}
}

func TestCompetitiveAIStandardStartProjectsTheCapacityRoster(t *testing.T) {
	catalog, err := mapdata.LoadCatalog(filepath.Join("..", "..", "..", "runtime", "client-patched"))
	if err != nil {
		t.Skipf("verified runtime client is unavailable: %v", err)
	}
	for _, test := range []struct {
		name        string
		selection   roomstate.MapSelection
		closedSeats []byte
		wantPlayers int
	}{
		{"fixed eight seats", roomstate.FixedMapSelection(905), nil, 8},
		{"fixed six seats", roomstate.FixedMapSelection(905), []byte{2, 6}, 6},
		{"random eight seats", roomstate.RandomMapSelection(), nil, 8},
		{"random odd capacity", roomstate.RandomMapSelection(), []byte{8}, 6},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := &Server{
				mapCatalog: catalog, logWriter: io.Discard,
				config:              Config{CompetitiveAI: CompetitiveAIConfig{Enabled: true}},
				competitiveAIPolicy: &competitiveAIActorPolicyFactoryProbe{},
				battles:             make(map[uint32]*match.AdventureBattle), competitiveBattles: make(map[uint32]*match.CompetitiveBattle),
			}
			profile := game.DefaultPlayerProfile()
			profile.GameInfo.RoleID = 7
			profile.GameInfo.Point = ^uint32(0)
			profile.Inventory = []game.ItemInfo{game.NewPermanentItemInfo(game.CompetitiveAICardItemID, 1)}
			owner := &connectionSession{UIN: 1_000_001, Profile: profile}
			if err := server.createSessionRoom(owner, 1, byte(roomstate.GameTypeCompetitiveNoItem)); err != nil {
				t.Fatal(err)
			}
			if _, err := server.worldState().UpdateMatchSettings(sessionWorldUIN(owner), roomstate.MatchSettings{
				Map: test.selection, GameType: roomstate.GameTypeCompetitiveNoItem,
			}); err != nil {
				t.Fatal(err)
			}
			room, err := server.sessionRoom(owner)
			if err != nil {
				t.Fatal(err)
			}
			for _, seatID := range test.closedSeats {
				if _, err := room.SetSeatLocked(owner.Profile.PlayerID, seatID, true); err != nil {
					t.Fatal(err)
				}
			}
			payload := make([]byte, 8)
			binary.BigEndian.PutUint32(payload[:4], owner.UIN)
			binary.BigEndian.PutUint32(payload[4:], uint32(owner.RoomID))
			request := testLocalRoutedPacketWithPayload(t, game.StartGameCommand, 3, 0xffff, 1, owner.UIN, payload)
			prepared, err := server.prepareCompetitiveRoomMatchStart(owner, request)
			if err != nil {
				t.Fatal(err)
			}
			packet, err := game.InspectLocalPacket(prepared.followUp)
			if err != nil {
				t.Fatal(err)
			}
			begin, err := game.ParseLengthPrefixedGameBeginDataNetwork(packet.Payload)
			if err != nil {
				t.Fatal(err)
			}
			if len(begin.Players) != test.wantPlayers {
				t.Fatalf("map %d GAME_BEGIN has %d players, want %d", begin.MapID, len(begin.Players), test.wantPlayers)
			}
			counts := make(map[byte]int)
			for _, player := range begin.Players {
				counts[player.TeamID]++
			}
			if len(counts) != 2 {
				t.Fatalf("GAME_BEGIN teams = %v, want two", counts)
			}
			for teamID, count := range counts {
				if count != test.wantPlayers/2 {
					t.Fatalf("GAME_BEGIN team %d has %d players, want %d", teamID, count, test.wantPlayers/2)
				}
			}
			if prepared.competitiveAI == nil || len(prepared.competitiveAI.roomProjections) != test.wantPlayers-1 {
				t.Fatalf("runtime is missing the projected AI roster: %+v", prepared.competitiveAI)
			}
			for _, projection := range prepared.competitiveAI.roomProjections {
				if room.Snapshot().LockedSeats[projection.SeatID-1] {
					t.Fatalf("AI occupied closed seat %d", projection.SeatID)
				}
			}
		})
	}
}

func TestPlanCompetitiveAIFillStandardBuildsThreeVersusThree(t *testing.T) {
	entry := testCompetitiveAIMap(6)
	snapshot := testCompetitiveAISnapshot(false, 6)
	humans := []match.CompetitiveParticipant{
		{PlayerID: 1, RoleID: 7, TeamID: 4},
		{PlayerID: 2, RoleID: 8, TeamID: 4},
		{PlayerID: 3, RoleID: 9, TeamID: 4},
	}
	plan, err := planCompetitiveAIFill(snapshot, entry, humans, 117)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Participants) != 3 {
		t.Fatalf("standard 3v3 fill count = %d, want 3", len(plan.Participants))
	}
	for _, participant := range plan.Participants {
		if participant.TeamID == 4 || participant.TeamID != plan.Participants[0].TeamID {
			t.Fatalf("standard 3v3 fill teams = %+v", plan.Participants)
		}
	}
}

func TestPlanCompetitiveAIFillStandardUsesLegacySingleSpawnPool(t *testing.T) {
	entry := testCompetitiveAIMap(6)
	entry.SpawnGroupA = append(entry.SpawnGroupA, entry.SpawnGroupB[:2]...)
	entry.SpawnGroupB = nil
	humans := []match.CompetitiveParticipant{
		{PlayerID: 1, RoleID: 7, TeamID: 4},
		{PlayerID: 2, RoleID: 8, TeamID: 4},
		{PlayerID: 3, RoleID: 9, TeamID: 4},
	}
	plan, err := planCompetitiveAIFill(testCompetitiveAISnapshot(false, 6), entry, humans, 117)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Participants) != 3 {
		t.Fatalf("single-pool standard 3v3 fill count = %d, want 3", len(plan.Participants))
	}
}

func TestPlanCompetitiveAIFillStandardNeverPartiallyFillsAnOpponentTeam(t *testing.T) {
	entry := testCompetitiveAIMap(3)
	snapshot := testCompetitiveAISnapshot(false, 3)
	humans := []match.CompetitiveParticipant{
		{PlayerID: 1, RoleID: 7, TeamID: 3},
		{PlayerID: 2, RoleID: 8, TeamID: 3},
	}
	plan, err := planCompetitiveAIFill(snapshot, entry, humans, 77)
	if err != nil || len(plan.Participants) != 0 {
		t.Fatalf("insufficient standard capacity produced a partial team: plan=%+v err=%v", plan, err)
	}
}

func TestPlanCompetitiveAIFillFreeUsesOpenMapCapacityAndMultipleColors(t *testing.T) {
	entry := testCompetitiveAIMap(8)
	snapshot := testCompetitiveAISnapshot(true, 6)
	humans := []match.CompetitiveParticipant{{PlayerID: 1, RoleID: 7, TeamID: 1}}
	plan, err := planCompetitiveAIFill(snapshot, entry, humans, 91)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Participants) != 5 {
		t.Fatalf("free fill count = %d, want 5", len(plan.Participants))
	}
	teams := map[byte]int{1: 1}
	for _, participant := range plan.Participants {
		if participant.TeamID == 1 {
			t.Fatalf("free fill placed AI on the sole human team: %+v", plan.Participants)
		}
		teams[participant.TeamID]++
		if teams[participant.TeamID] > 1 {
			t.Fatalf("solo free room created an AI alliance: %+v", plan.Participants)
		}
	}
	if len(teams) < 2 {
		t.Fatalf("free fill produced no opponent: %+v", plan.Participants)
	}
}

func TestPlanCompetitiveAIFillFreeCapsEveryAITeamAtHumanTeamSize(t *testing.T) {
	entry := testCompetitiveAIMap(8)
	snapshot := testCompetitiveAISnapshot(true, 8)
	humans := []match.CompetitiveParticipant{
		{PlayerID: 1, RoleID: 7, TeamID: 1},
		{PlayerID: 2, RoleID: 8, TeamID: 1},
	}
	for seed := uint64(1); seed <= 32; seed++ {
		plan, err := planCompetitiveAIFill(snapshot, entry, humans, seed)
		if err != nil {
			t.Fatal(err)
		}
		if len(plan.Participants) != 6 {
			t.Fatalf("seed %d free fill count = %d, want 6", seed, len(plan.Participants))
		}
		counts := make(map[byte]int)
		for _, participant := range plan.Participants {
			if participant.TeamID == 1 {
				t.Fatalf("seed %d put AI on human team: %+v", seed, plan.Participants)
			}
			counts[participant.TeamID]++
			if counts[participant.TeamID] > len(humans) {
				t.Fatalf("seed %d AI team exceeds human team size: %+v", seed, plan.Participants)
			}
		}
	}
}

func TestPlanCompetitiveAIFillLeavesBossAndSpecialRulesUnchanged(t *testing.T) {
	snapshot := testCompetitiveAISnapshot(false, 2)
	humans := []match.CompetitiveParticipant{{PlayerID: 1, RoleID: 7, TeamID: 1}}
	entry := testCompetitiveAIMap(2)
	entry.NativeRule = 2
	entry.Rule = mapdata.CompetitiveRuleKickBomb
	plan, err := planCompetitiveAIFill(snapshot, entry, humans, 1)
	if err != nil || len(plan.Participants) != 0 {
		t.Fatalf("special native rule changed by virtual AI: plan=%+v err=%v", plan, err)
	}
	entry = testCompetitiveAIMap(2)
	entry.RequiredItemField = 1
	plan, err = planCompetitiveAIFill(snapshot, entry, humans, 1)
	if err != nil || len(plan.Participants) != 0 {
		t.Fatalf("item field changed by virtual AI: plan=%+v err=%v", plan, err)
	}
}

func TestPlanCompetitiveAIFillLeavesExistingMultiTeamRoomUnchanged(t *testing.T) {
	plan, err := planCompetitiveAIFill(testCompetitiveAISnapshot(true, 8), testCompetitiveAIMap(8), []match.CompetitiveParticipant{
		{PlayerID: 1, RoleID: 7, TeamID: 1}, {PlayerID: 2, RoleID: 8, TeamID: 2},
	}, 9)
	if err != nil || len(plan.Participants) != 0 {
		t.Fatalf("existing multi-team plan = %+v, %v", plan, err)
	}
}

func TestCompetitiveControlCardAdmissionIsOrdinaryUnionRegisteredBoss(t *testing.T) {
	ordinary := testCompetitiveAIMap(8)
	if !competitiveMapAllowsControlCardAdmission(ordinary) {
		t.Fatal("ordinary gameplay map was excluded from control-card admission")
	}
	football := mapdata.CompetitiveMap{
		ID: 701, NativeRule: 2, Rule: mapdata.CompetitiveRuleKickBomb,
		BossCandidates: []mapdata.CompetitiveBossCandidate{{ID: "rooney"}},
	}
	if !competitiveMapAllowsControlCardAdmission(football) {
		t.Fatal("registered football Boss map was excluded from control-card admission")
	}
	football.BossCandidates = nil
	if competitiveMapAllowsControlCardAdmission(football) {
		t.Fatal("non-Boss kick-bomb map bypassed control-card admission")
	}
	machine := mapdata.CompetitiveMap{ID: 1301, NativeRule: 7, Rule: mapdata.CompetitiveRuleMachine}
	if competitiveMapAllowsControlCardAdmission(machine) {
		t.Fatal("non-Boss machine map bypassed control-card admission")
	}
}

func testCompetitiveAISnapshot(free bool, capacity byte) roomstate.Snapshot {
	var locked [roomstate.RoomSeatCount]bool
	for index := int(capacity); index < len(locked); index++ {
		locked[index] = true
	}
	properties := roomstate.Properties{}
	if free {
		properties.Flag = roomstate.PropertyFlagFreeRule
	}
	return roomstate.Snapshot{RoomID: 7, Properties: properties, Settings: roomstate.MatchSettings{PlayerLimit: 8}, LockedSeats: locked}
}

func testCompetitiveAIMap(limit byte) mapdata.CompetitiveMap {
	cells := make([]mapdata.CompetitiveBattleCell, 8)
	for index := range cells {
		cells[index] = mapdata.CompetitiveBattleCell{Collision: mapdata.CompetitiveCellOpen, FlamePassable: true}
	}
	return mapdata.CompetitiveMap{
		ID: 7001, PlayerLimit: limit, NativeRule: 1, Rule: mapdata.CompetitiveRuleOrdinary,
		Battlefield: mapdata.CompetitiveBattlefield{
			Width: 4, Height: 2, Cells: cells,
		},
		SpawnGroupA: []mapdata.CompetitiveCell{{Row: 0, Col: 0}, {Row: 0, Col: 1}, {Row: 1, Col: 0}, {Row: 1, Col: 1}},
		SpawnGroupB: []mapdata.CompetitiveCell{{Row: 0, Col: 2}, {Row: 0, Col: 3}, {Row: 1, Col: 2}, {Row: 1, Col: 3}},
	}
}
