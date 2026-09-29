package mapdata

import (
	"math"
	"sort"

	"qqtang/internal/game/rolecatalog"
)

const CompetitiveSupplyVersion = "public-attribute-supply-v1"

// Native initial table capacity. A bird serializes <=32 delayed entries;
// remaining entries wait for later batches instead of becoming endless supply.
const CompetitiveDelayedSupplyLimit = 64

type CompetitiveSupply struct {
	WallItems       []CompetitiveWallItem
	DelayedItems    []CompetitiveWallItem
	DesiredBasic    [3]int
	UnfilledBasic   [3]int
	OtherReserved   int
	WallLimit       int
	CapacityLimited bool
}

// HasNativeDelayedItemSupply describes native support, not our local budget
// policy. Kick-bomb's loader never initializes Items; treasure1111 skips it.
func (entry CompetitiveMap) HasNativeDelayedItemSupply() bool {
	if entry.ID == 1111 {
		return false
	}
	switch entry.NativeRule {
	case 1, 3, 4, 5, 6, 7, 8, 13:
		return true
	}
	return false
}

// PlanCompetitiveSupply uses the starting roster and public map pool only.
// It never reads who is losing, who collected items, or human/AI identity.
// Ordinary-rule wall budgets follow the experience design; objective-rule
// wall lists stay intact. Wrestle has its own 15s supply and vehicles/empty
// native pools are not assigned ordinary-player attribute targets.
func (entry CompetitiveMap) PlanCompetitiveSupply(seed uint32, roles []uint16, boss bool) CompetitiveSupply {
	h := len(entry.HiddenItemCells)
	n := len(roles)
	plan := CompetitiveSupply{}
	if boss {
		plan.WallItems = entry.RollFinalCompetitiveBossWallItems(seed, n, h, h/2)
	} else {
		plan.WallItems = entry.RollFinalCompetitiveOrdinaryWallItems(seed, n, h, h/2)
	}
	if n < 1 || n > 8 || h == 0 || len(entry.WallItemRules) == 0 {
		return plan
	}
	switch entry.NativeRule {
	case 1, 3, 5, 6:
	default:
		return plan
	}
	deficits := [3]int{}
	for _, id := range roles {
		profile, ok := rolecatalog.PlayableCombatProfile(id)
		if !ok {
			// Synthetic fixtures and unsupported role models keep the old pool.
			return plan
		}
		deficits[0] += int(profile.MaxBombCapacity) - int(profile.BombCapacity)
		deficits[1] += int(profile.MaxBombPower) - int(profile.BombPower)
		deficits[2] += int(profile.MaxSpeedRate) - int(profile.SpeedRate)
	}
	factor := 1.0
	if !boss {
		switch {
		case n <= 2:
			factor = 1.5
		case n == 3:
			factor = 1.35
		case n == 4:
			factor = 1.2
		default:
			factor = float64((2*n+2)/3) / (0.8 * float64(n))
		}
	}
	eligible := [3]bool{}
	for _, r := range entry.WallItemRules {
		if r.SceneID >= 1 && r.SceneID <= 3 && r.Probability > 0 && r.Maximum > 0 {
			eligible[r.SceneID-1] = true
		}
	}
	quantity := map[uint32]int{}
	for _, item := range plan.WallItems {
		quantity[item.SceneID] += int(item.Quantity)
	}
	for i, allowed := range eligible {
		if allowed {
			plan.DesiredBasic[i] = max(quantity[uint32(i)+1], int(math.Ceil(float64(deficits[i])*factor)))
		}
	}
	if entry.NativeRule == 1 {
		other := 0
		for id, count := range quantity {
			if !(id >= 1 && id <= 3) && !(id >= 6 && id <= 8) {
				other += count
			}
		}
		// The existing Boss fork is counted in the protected other-item pool;
		// restore upper floors AFTER that allocation rather than stealing one.
		plan.OtherReserved = max((h+3)/4, other)
		upper := [3]int{}
		for i, allowed := range eligible {
			upper[i] = quantity[uint32(i)+6]
			if allowed {
				floor := (n + 3) / 4
				if boss {
					floor = (n + 1) / 2
				}
				upper[i] = max(upper[i], floor)
			}
		}
		// The limit applies to ALL wall items, not attributes alone. Keep
		// roughly30% of normal walls empty and move the remaining stat budget
		// to birds. A rare conflict with existing functional/Boss stock is
		// reported explicitly instead of deleting that stock to hit a ratio.
		upperCount := upper[0] + upper[1] + upper[2]
		plan.WallLimit = h * 7 / 10
		if other+upperCount > plan.WallLimit {
			plan.WallLimit = other + upperCount
			plan.CapacityLimited = true
		}
		room := min(h-plan.OtherReserved-upperCount, plan.WallLimit-other-upperCount)
		if room < 0 {
			// No silent destruction of functional stock on very small maps.
			plan.CapacityLimited = true
		} else {
			basic := allocateAttributeSupply(plan.DesiredBasic, room)
			for i := range basic {
				quantity[uint32(i)+1] = basic[i]
				quantity[uint32(i)+6] = upper[i]
			}
			plan.WallItems = sortedSupplyItems(quantity)
		}
	}
	missing := [3]int{}
	for i := range missing {
		missing[i] = max(0, plan.DesiredBasic[i]-quantity[uint32(i)+1])
	}
	delayed := [3]int{}
	if entry.HasNativeDelayedItemSupply() {
		delayed = allocateAttributeSupply(missing, CompetitiveDelayedSupplyLimit)
	}
	for i, count := range delayed {
		if count > 0 {
			plan.DelayedItems = append(plan.DelayedItems, CompetitiveWallItem{SceneID: uint32(i) + 1, Quantity: int16(count)})
		}
		plan.UnfilledBasic[i] = missing[i] - count
	}
	return plan
}

func sortedSupplyItems(quantity map[uint32]int) []CompetitiveWallItem {
	ids := make([]uint32, 0, len(quantity))
	for id, q := range quantity {
		if q > 0 {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	result := make([]CompetitiveWallItem, 0, len(ids))
	for _, id := range ids {
		result = append(result, CompetitiveWallItem{SceneID: id, Quantity: int16(quantity[id])})
	}
	return result
}

// Proportional integer allocation with largest remainders; stable tie order.
func allocateAttributeSupply(want [3]int, limit int) (got [3]int) {
	total := want[0] + want[1] + want[2]
	if total <= limit {
		return want
	}
	if total <= 0 || limit <= 0 {
		return got
	}
	remainders := [3]int{}
	used := 0
	for i, w := range want {
		got[i] = w * limit / total
		remainders[i] = w * limit % total
		used += got[i]
	}
	for used < limit {
		best := 0
		for i := 1; i < 3; i++ {
			if remainders[i] > remainders[best] {
				best = i
			}
		}
		got[best]++
		remainders[best] = -1
		used++
	}
	return got
}
