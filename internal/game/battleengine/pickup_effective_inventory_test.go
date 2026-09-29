package battleengine

import "testing"

func TestPickupReportsActualInventoryDelta(t *testing.T) {
	for _, test := range []struct {
		name   string
		before uint8
		want   uint8
	}{
		{"new", 0, 3}, {"partial saturation", 254, 255}, {"already saturated", 255, 255},
	} {
		t.Run(test.name, func(t *testing.T) {
			actor := Actor{Participant: Participant{PlayerID: 1}}
			if test.before > 0 {
				actor.HeldActions[0] = HeldActionSlot{ActionID: 43, Count: test.before}
			}
			engine := &Engine{}
			// Native SceneID 25 grants action 43 x3; verify the definition below
			// through the event rather than changing the native inventory cap.
			pickup := Pickup{SceneID: 25, State: PickupAvailable}
			event := engine.collectPickup(&actor, &pickup)
			if event.ActionID != 43 || event.ActionCount != 3 || event.ValueBefore != test.before || event.ValueAfter != test.want {
				t.Fatalf("pickup event: %+v", event)
			}
			if actorHeldActionCount(&actor, 43) != test.want || pickup.State != PickupCollected {
				t.Fatal("native grant or consumption changed")
			}
		})
	}
}
