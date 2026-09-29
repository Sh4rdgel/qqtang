package probe

import "qqtang/internal/protocol/game"

// Schema clocks identify occurrence; the enclosing package clock identifies
// sending. In particular FB4's 16-bit kick clock must not be read as an
// absolute 32-bit round clock. Unrecovered clocks keep the message timestamp.
func competitiveAIEventClock(message game.BattleMessageData) uint32 {
	e := game.GameEvent{Schema: uint16(message.DataID), Body: message.Data}
	switch e.Schema {
	case game.NotifyBombExplode:
		if v, err := game.ParseBombExplodeEvent(e); err == nil {
			return v.ClientTime
		}
	case game.NotifyDispatchItem:
		if v, err := game.ParseDispatchItemEvent(e); err == nil {
			return v.Time
		}
	case game.NotifyItemExploded:
		if v, err := game.ParseItemsExplodedEvent(e); err == nil {
			return v.Time
		}
	case game.NotifyMapElementMoved:
		if v, err := game.ParseMapElementMovedEvent(e); err == nil {
			return v.ClientTime
		}
	case game.PlayerBeExploded:
		if v, err := game.ParsePlayerExplodedEvent(e); err == nil {
			return v.ClientTime
		}
	case game.NotifyPlayerExploded:
		if v, err := game.ParsePlayerExplodedNotification(e); err == nil {
			return v.ClientTime
		}
	case game.NotifyRecoverAvatar:
		if v, err := game.ParseAvatarRecoveryEvent(e); err == nil {
			return v.Time
		}
	case game.NotifyPlayerGetItem:
		if v, err := game.ParsePlayerItemEvent(e); err == nil {
			return v.ClientTime
		}
	case game.NotifyPlayerSaved:
		if v, err := game.ParsePlayerInteractionEvent(e); err == nil {
			return v.ClientTime
		}
	case game.NotifyPlayerKilled:
		if v, err := game.ParsePlayerKilledEvent(e); err == nil {
			return v.ClientTime
		}
	case game.NotifyPlayerDieEvent:
		if v, err := game.ParsePlayerDeathEvent(e); err == nil {
			return v.ClientTime
		}
	}
	return message.Time
}
