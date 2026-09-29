package battleengine

// HeldActionGrantAmount is the public inventory consequence of a native grant.
// Observation and mutation share it, including saturation and seven-slot limits.
func HeldActionGrantAmount(slots [NativeBattleActionSlots]HeldActionSlot, actionID, count uint8) uint8 {
	_, gain := heldActionGrantSlot(slots, actionID, count)
	return gain
}

func heldActionGrantSlot(slots [NativeBattleActionSlots]HeldActionSlot, actionID, count uint8) (int, uint8) {
	if actionID == 0 || count == 0 {
		return -1, 0
	}
	empty := -1
	for index, slot := range slots {
		if slot.ActionID == actionID {
			remaining := ^uint8(0) - slot.Count
			if count > remaining {
				return index, remaining
			}
			return index, count
		}
		if empty < 0 && slot.ActionID == 0 {
			empty = index
		}
	}
	if empty >= 0 {
		return empty, count
	}
	return -1, 0
}

func actorHeldActionCount(actor *Actor, actionID uint8) uint8 {
	if actor == nil || actionID == 0 {
		return 0
	}
	for _, slot := range actor.HeldActions {
		if slot.ActionID == actionID {
			return slot.Count
		}
	}
	return 0
}

// grantHeldAction mirrors the native seven-slot inventory: an existing action
// stacks in place, otherwise the first empty slot is used. A full inventory
// rejects the grant while the scene pickup itself may still be consumed.
func grantHeldAction(actor *Actor, actionID, count uint8) bool {
	if actor == nil || actionID == 0 || count == 0 {
		return false
	}
	index, gain := heldActionGrantSlot(actor.HeldActions, actionID, count)
	if index < 0 {
		return false
	}
	actor.HeldActions[index].ActionID = actionID
	actor.HeldActions[index].Count += gain
	return true
}

func consumeHeldAction(actor *Actor, actionID uint8) bool {
	if actor == nil || actionID == 0 {
		return false
	}
	for index := range actor.HeldActions {
		slot := &actor.HeldActions[index]
		if slot.ActionID != actionID || slot.Count == 0 {
			continue
		}
		slot.Count--
		if slot.Count == 0 {
			copy(actor.HeldActions[index:], actor.HeldActions[index+1:])
			actor.HeldActions[len(actor.HeldActions)-1] = HeldActionSlot{}
		}
		return true
	}
	return false
}

func removeHeldAction(actor *Actor, actionID uint8) {
	if actor == nil || actionID == 0 {
		return
	}
	for actorHeldActionCount(actor, actionID) != 0 {
		for index := range actor.HeldActions {
			if actor.HeldActions[index].ActionID != actionID {
				continue
			}
			copy(actor.HeldActions[index:], actor.HeldActions[index+1:])
			actor.HeldActions[len(actor.HeldActions)-1] = HeldActionSlot{}
			break
		}
	}
}
