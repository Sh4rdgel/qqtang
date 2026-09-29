package battleengine

// ItemState is factual state, not an item price or a predicted win. Base
// attributes and effective temporary capabilities intentionally stay separate.
type ItemState struct {
	State                                            ActorState
	BaseCapacity, BasePower, BaseSpeed               byte
	EffectiveCapacity                                byte
	HorizontalSpeed, VerticalSpeed                   uint16
	ActiveBombs                                      byte
	TransformationSceneID                            uint32
	TransformationRemainingMS, ProtectionRemainingMS uint32
	MovementStatus                                   MovementStatusKind
	MovementRemainingMS, DetectorRemainingMS         uint32
	CanCollectItems                                  bool
	HeldActions                                      [NativeBattleActionSlots]HeldActionSlot
}

type ItemStateChange struct {
	Before, After ItemState
}

func (engine *Engine) itemState(actor *Actor) ItemState {
	remaining := func(expires uint32) uint32 {
		if expires > engine.elapsedMS {
			return expires - engine.elapsedMS
		}
		return 0
	}
	capabilities := engine.actorCapabilities(actor, actor.Facing)
	return ItemState{
		State: actor.State, BaseCapacity: actor.BombCapacity, BasePower: actor.BombPower, BaseSpeed: actor.SpeedRate,
		EffectiveCapacity:         capabilities.EffectiveBombCapacity,
		HorizontalSpeed:           engine.effectiveSpeedPixelsPerSecond(actor, DirectionRight),
		VerticalSpeed:             engine.effectiveSpeedPixelsPerSecond(actor, DirectionDown),
		ActiveBombs:               byte(engine.activeBombCount(actor.PlayerID)),
		TransformationSceneID:     actor.TransformationSceneID,
		TransformationRemainingMS: remaining(actor.TransformationExpiresAt),
		ProtectionRemainingMS:     remaining(actor.HarmProtectionExpiresAt),
		MovementStatus:            actor.MovementStatus, MovementRemainingMS: remaining(actor.MovementStatusExpiresAt),
		DetectorRemainingMS: remaining(actor.HiddenPickupReachExpiresAt),
		CanCollectItems:     capabilities.CanCollectItems, HeldActions: actor.HeldActions,
	}
}

func (engine *Engine) beginItemChange(actor *Actor) *ItemStateChange {
	if !engine.rules.RecordItemEffects {
		return nil
	}
	return &ItemStateChange{Before: engine.itemState(actor)}
}

func (engine *Engine) finishItemChange(change *ItemStateChange, actor *Actor) {
	if change != nil {
		change.After = engine.itemState(actor)
	}
}
