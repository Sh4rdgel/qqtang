package probe

import (
	"bytes"
	"io"
	"qqtang/internal/game/battleengine"
	"qqtang/internal/game/match"
	"qqtang/internal/protocol/game"
	"testing"
	"time"
)

func TestAuthorityBatchAppliesEarlierHitBeforeLaterSimulation(t *testing.T) {
	participants := []match.CompetitiveParticipant{
		{PlayerID: 1, RoleID: 1, TeamID: 1, Source: match.CompetitiveParticipantHuman},
		{PlayerID: 20001, RoleID: 2, TeamID: 2, Source: match.CompetitiveParticipantVirtualAI},
	}
	seenTrapped := false
	runtime, err := newLiveCompetitiveAIRuntime(7, testCompetitiveAIMap(2), game.GameBeginData{GameID: 106, SpawnSeed: 11, ItemSeed: 22}, participants, false, battleengine.PolicyFunc(func(o battleengine.Observation, _ []battleengine.Action) (battleengine.Action, error) {
		if o.ClockMS >= 3200 {
			for _, actor := range o.Actors {
				if actor.PlayerID == 1 && actor.State == battleengine.ActorTrapped {
					seenTrapped = true
				}
			}
		}
		return battleengine.Action{PlayerID: o.PlayerID}, nil
	}), 100)
	if err != nil {
		t.Fatal(err)
	}
	battle, err := match.NewCompetitiveBattle(106, 2, 1, participants)
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{competitiveAIRuntime: map[uint32]*liveCompetitiveAIRuntime{106: runtime}, competitiveBattles: map[uint32]*match.CompetitiveBattle{106: battle}, logWriter: io.Discard}
	human, _ := actorByID(runtime.runtime.EngineSnapshot().Actors(), 1)
	body, err := (game.PlayerExplodedEvent{PlayerID: 1, ClientTime: 3200, PosX: uint16(human.Position.X), PosY: uint16(human.Position.Y)}).MarshalNetworkBinary()
	if err != nil {
		t.Fatal(err)
	}
	dispatch, err := (game.DispatchItemData{Time: 3400}).MarshalNetworkBinary()
	if err != nil {
		t.Fatal(err)
	}
	server.recordCompetitiveAIHumanPackages(106, []game.GameplayDataPackage{{PlayerID: 1, Time: 3400, GameID: 106, Messages: []game.BattleMessageData{
		{Time: 3400, DataID: uint32(game.PlayerBeExploded), Sequence: 1, Data: body},
		{Time: 3400, DataID: uint32(game.NotifyDispatchItem), Sequence: 2, Data: dispatch},
	}}})
	if !seenTrapped {
		t.Fatal("later frames simulated before earlier FA5, or ignored schema occurrence clock")
	}
	if got := runtime.runtime.EngineSnapshot().ElapsedMS(); got != 3400 {
		t.Fatalf("elapsed=%d", got)
	}
}

func TestNativeUDPReceiveWindowAllowsWrapAndIndependentPlayers(t *testing.T) {
	runtime := &liveCompetitiveAIRuntime{}
	for _, v := range []struct {
		player uint16
		seq    uint32
		want   bool
	}{{1, 0xfffffffe, true}, {1, 1, true}, {1, 0xffffffff, false}, {1, 1, false}, {2, 0, true}, {1, 2, true}} {
		if got := runtime.acceptNativeUDPPacket(v.player, v.seq); got != v.want {
			t.Fatalf("%+v got=%t", v, got)
		}
	}
}

func TestHitRetryKeepsSceneIdentityAndBoundsFailure(t *testing.T) {
	runtime := &liveCompetitiveAIRuntime{hitRequests: make(map[liveCompetitiveAIHitRequestKey]liveCompetitiveAIHitRequest)}
	now := time.Unix(100, 0)
	hit := game.PlayerExplodedEvent{PlayerID: 20001, ClientTime: 3400, PosX: 60, PosY: 60}
	runtime.reserveHitRequest(hit, 1, now)
	key := competitiveAIHitKey(hit)
	request := runtime.hitRequests[key]
	request.payload = []byte{1, 2, 3}
	runtime.hitRequests[key] = request
	for i := 1; i < competitiveAIHitMaxAttempts; i++ {
		due := runtime.dueHitRetries(now.Add(time.Duration(i) * competitiveAIHitRetryInterval))
		if len(due) != 1 || due[0].key != key || !bytes.Equal(due[0].payload, request.payload) || due[0].exhausted {
			t.Fatalf("retry %d=%+v", i, due)
		}
	}
	due := runtime.dueHitRetries(now.Add(competitiveAIHitMaxAttempts * competitiveAIHitRetryInterval))
	if len(due) != 1 || !due[0].exhausted || runtime.hitRequests[key].state != competitiveAIHitRecoveryRequired {
		t.Fatalf("missing explicit recovery state: %+v", due)
	}
	if len(runtime.dueHitRetries(now.Add(time.Hour))) != 0 {
		t.Fatal("retry budget repeated")
	}
	runtime.reserveHitRequest(hit, 1, now)
	request = runtime.hitRequests[key]
	request.state = competitiveAIHitCommitted
	runtime.hitRequests[key] = request
	if len(runtime.dueHitRetries(now.Add(time.Hour))) != 0 {
		t.Fatal("confirmed hit retried")
	}
}

func TestOrdinaryItemTurnKeepsIntervalOrigin(t *testing.T) {
	engine, projection := turnTestEngine(t, battleengine.DirectionRight)
	before := engine.Clone()
	origin, _ := actorByID(before.Actors(), 20001)
	action := battleengine.Action{PlayerID: 20001, Move: battleengine.DirectionUp}
	if _, err := engine.Step([]battleengine.Action{action}); err != nil {
		t.Fatal(err)
	}
	// Item with no movement/form change: the samples consume its action pulse,
	// while before/after already contain the effect produced by the engine.
	action.UseActionID = 44
	_, moves, err := competitiveAIMovementFrameSamples(before, engine, 20001, action, projection, false)
	if err != nil || len(moves) != 1 || moves[0].TimeStamp != before.ElapsedMS() || moves[0].CurrentPosX != uint16(origin.Position.X) || moves[0].CurrentPosY != uint16(origin.Position.Y) {
		t.Fatalf("item turn=%+v err=%v", moves, err)
	}
}
