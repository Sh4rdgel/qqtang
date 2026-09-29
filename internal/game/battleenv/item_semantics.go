package battleenv

import (
	"qqtang/internal/clientdata/sceneelement"
	"qqtang/internal/game/battleengine"
)

type ItemEventRecord struct {
	Kind                    string
	SubjectID, SourceID     uint16
	SubjectTeam, SourceTeam byte
	SourceRelation          string
	Event                   battleengine.Event
}

func itemEventRecord(event battleengine.Event, actors []battleengine.Actor) ItemEventRecord {
	r := ItemEventRecord{SubjectID: event.PlayerID, SourceID: event.PlayerID, Event: event}
	switch event.Kind {
	case battleengine.EventPickupCollected:
		r.Kind = "pickup"
	case battleengine.EventBattleActionUsed:
		r.Kind = "use"
	case battleengine.EventActorTransformationEnded:
		r.Kind, r.SourceID = "transformation_expired", 0
		if event.TransformationEnd == battleengine.TransformationEndHit {
			r.Kind, r.SourceID = "transformation_absorbed_hit", event.TargetID
		}
	case battleengine.EventFieldObjectTriggered:
		r.Kind, r.SubjectID = "field_trigger", event.TargetID
	case battleengine.EventMovementStatusEnded:
		r.Kind, r.SourceID = "movement_ended", 0
	}
	for _, actor := range actors {
		if actor.PlayerID == r.SubjectID {
			r.SubjectTeam = actor.TeamID
		}
		if actor.PlayerID == r.SourceID {
			r.SourceTeam = actor.TeamID
		}
	}
	r.SourceRelation = "unknown"
	switch {
	case r.SourceID == 0:
		r.SourceRelation = "none"
	case r.SubjectID == r.SourceID:
		r.SourceRelation = "self"
	case r.SubjectTeam != 0 && r.SourceTeam != 0:
		if r.SubjectTeam == r.SourceTeam {
			r.SourceRelation = "ally"
		} else {
			r.SourceRelation = "enemy"
		}
	}
	return r
}

// Called only for already-visible pickups. It neither previews a cloned world
// nor reads hidden allocation. The old 101 planes remain byte-for-byte intact.
func setVisibleItemSemantics(set func(int, battleengine.Cell, float32), pickup battleengine.Pickup, self battleengine.ActorObservation) {
	if channel, ok := transformationChannel(pickup.SceneID); ok {
		set(pickupTransformationStart+channel-26, pickup.Cell, 1)
	}
	switch pickup.SceneID {
	case battleengine.SceneRandomAttribute:
		set(pickupQuestionChannel, pickup.Cell, 1)
	case battleengine.SceneHiddenPickupReach:
		set(pickupDetectorChannel, pickup.Cell, 1)
	case battleengine.SceneFastMovement:
		set(pickupTemporarySpeedChannel, pickup.Cell, 1)
	}
	collectable := pickup.State == battleengine.PickupAvailable && self.State == battleengine.ActorActive && self.Capabilities.CanCollectItems
	if !collectable {
		return
	}
	// Collection permission is not a path/safety recommendation. A full
	// inventory still consumes the object, so gain is recorded separately.
	set(pickupCollectableChannel, pickup.Cell, 1)
	if action, ok := sceneelement.NativeBattleActionPickup(sceneelement.ID(pickup.SceneID)); ok {
		gain := battleengine.HeldActionGrantAmount(self.HeldActions, action.ActionID, action.Count)
		set(pickupInventoryGainChannel, pickup.Cell, float32(gain)/9)
	}
}
