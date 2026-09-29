package battleengine

import (
	"fmt"

	"qqtang/internal/game/mapdata"
)

const NativePickupDispatchMaximum = 64

// NativeScheduledPickup is private future supply until the arbitrator sends
// a dispatch. DueMS is an absolute scene time, before bird flight/landing.
type NativeScheduledPickup struct {
	SceneID uint32
	DueMS   uint32
}

// RemainingScheduledPickupCount is diagnostic stock accounting. Due times
// never enter Observation; PolicySnapshot also removes the private schedule.
func (engine *Engine) RemainingScheduledPickupCount() int {
	if engine == nil {
		return 0
	}
	return len(engine.scheduledPickups)
}

func ScheduledPickupsFromItems(seed uint32, items []mapdata.CompetitiveWallItem) ([]NativeScheduledPickup, error) {
	if len(items) > 32 {
		return nil, fmt.Errorf("native delayed supply has more than 32 item types")
	}
	count := 0
	for _, item := range items {
		if _, _, ok := supportedPickupEffect(item.SceneID); !ok || item.Quantity <= 0 {
			return nil, fmt.Errorf("invalid native delayed item %+v", item)
		}
		count += int(item.Quantity)
		if count > NativePickupDispatchMaximum {
			return nil, fmt.Errorf("native delayed supply exceeds 64 expanded items")
		}
	}
	rng := nativeMapRNG{state: seed}
	result := make([]NativeScheduledPickup, 0, count)
	for _, item := range items {
		for n := 0; n < int(item.Quantity); n++ {
			result = append(result, NativeScheduledPickup{SceneID: item.SceneID, DueMS: (rng.next()%10 + 6) * 10_000})
		}
	}
	return result, nil
}

func validateScheduledPickups(items []NativeScheduledPickup) error {
	if len(items) > NativePickupDispatchMaximum {
		return fmt.Errorf("native delayed supply exceeds 64 expanded items")
	}
	for _, item := range items {
		if _, _, ok := supportedPickupEffect(item.SceneID); !ok || item.DueMS < 60_000 || item.DueMS > 150_000 || item.DueMS%10_000 != 0 {
			return fmt.Errorf("invalid native scheduled item %+v", item)
		}
	}
	return nil
}
