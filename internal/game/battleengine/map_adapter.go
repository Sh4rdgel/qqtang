package battleengine

import (
	"fmt"

	"qqtang/internal/clientdata/sceneelement"
	"qqtang/internal/game/mapdata"
)

// GridFromCompetitiveMap accepts only the native ordinary rule. Boss overlays
// may share this base grid, but Boss objects and their AI remain outside the
// restricted engine.
func GridFromCompetitiveMap(entry mapdata.CompetitiveMap) (Grid, error) {
	if entry.NativeRule != 1 || entry.Rule != mapdata.CompetitiveRuleOrdinary {
		return Grid{}, fmt.Errorf("competitive map %d uses native rule %d/%s, want ordinary rule 1", entry.ID, entry.NativeRule, entry.Rule)
	}
	field := entry.Battlefield
	if field.Width == 0 || field.Height == 0 || len(field.Cells) != int(field.Width)*int(field.Height) {
		return Grid{}, fmt.Errorf("competitive map %d has no valid battlefield collision grid", entry.ID)
	}
	grid := Grid{Width: field.Width, Height: field.Height, Cells: make([]Tile, len(field.Cells))}
	for index, cell := range field.Cells {
		grid.Cells[index].FlamePassable = cell.FlamePassable
		grid.Cells[index].NativeGridAttr = cell.NativeGridAttr
		grid.Cells[index].NativeGridAttrSet = cell.NativeGridAttrSet
		grid.Cells[index].NativePlayerExitAttr = cell.NativePlayerExitAttr
		grid.Cells[index].NativePlayerExitAttrSet = cell.NativePlayerExitAttrSet
		grid.Cells[index].MapElementOccupied = cell.MapElementOccupied
		grid.Cells[index].Durability = cell.Durability
		grid.Cells[index].MapElementID = cell.MapElementID
		grid.Cells[index].NormalPushable = cell.NormalPushable
		grid.Cells[index].PandaPushable = cell.PandaPushable
		grid.Cells[index].ElementWidth = cell.ElementWidth
		grid.Cells[index].ElementHeight = cell.ElementHeight
		grid.Cells[index].ElementAnchor = Cell{Row: cell.ElementAnchorRow, Col: cell.ElementAnchorCol}
		switch cell.Collision {
		case mapdata.CompetitiveCellOpen:
			grid.Cells[index].Kind = CellOpen
		case mapdata.CompetitiveCellBreakable:
			grid.Cells[index].Kind = CellBreakable
		case mapdata.CompetitiveCellSolid:
			grid.Cells[index].Kind = CellSolid
		default:
			return Grid{}, fmt.Errorf("competitive map %d cell %d has unknown collision %d", entry.ID, index, cell.Collision)
		}
	}
	return grid, nil
}

// ValidateNativeRuntimeMap reports whether every possible rule-1 input of a
// map is covered by the restricted engine. The check is static: eligibility
// must not change with ItemSeed merely because an unsupported wall-item rule
// happened not to roll in one match.
func ValidateNativeRuntimeMap(entry mapdata.CompetitiveMap) error {
	_, err := validateNativeRuntimeMap(entry)
	return err
}

func validateNativeRuntimeMap(entry mapdata.CompetitiveMap) (Grid, error) {
	grid, err := GridFromCompetitiveMap(entry)
	if err != nil {
		return Grid{}, err
	}
	if entry.PlayerLimit != 0 && entry.PlayerLimit < 2 {
		return Grid{}, fmt.Errorf("competitive map %d supports fewer than two participants", entry.ID)
	}
	if err := validateSupportedWallItemRules(entry); err != nil {
		return Grid{}, err
	}
	return grid, nil
}

func validateSupportedWallItemRules(entry mapdata.CompetitiveMap) error {
	for index, rule := range entry.WallItemRules {
		if rule.Probability <= 0 || rule.Maximum <= 0 || rule.Minimum > rule.Maximum {
			continue
		}
		if _, _, supported := supportedPickupEffect(rule.SceneID); !supported {
			return fmt.Errorf("competitive map %d wall-item rule %d may emit unsupported scene ID %d", entry.ID, index, rule.SceneID)
		}
	}
	return nil
}

// CompetitiveMapConfigOptions contains the two seeds carried by GAME_BEGIN and
// the participant projection supplied by the room/server layer. SimulationSeed
// is reserved for future engine-owned stochastic decisions; when zero it is
// derived deterministically from SpawnSeed and ItemSeed.
type CompetitiveMapConfigOptions struct {
	SimulationSeed uint64
	SpawnSeed      uint32
	ItemSeed       uint32
	SpawnMode      NativeSpawnMode
	Rules          Rules
	Participants   []Participant
	// RecordedWallItems preserves the exact GAME_BEGIN.NewItems wire order for
	// differential replay. UseRecordedWallItems distinguishes an explicitly
	// empty recorded list from the normal live-server quantity roll.
	RecordedWallItems    []mapdata.CompetitiveWallItem
	RecordedDelayedItems []mapdata.CompetitiveWallItem
	UseRecordedWallItems bool
}

// ConfigFromCompetitiveMap creates the complete deterministic rule-1 initial
// state from the native map tables and GAME_BEGIN seeds. Spawn coordinates and
// hidden-item candidates come from the serialized .map vectors rather than a
// geometric approximation.
func ConfigFromCompetitiveMap(entry mapdata.CompetitiveMap, options CompetitiveMapConfigOptions) (Config, error) {
	if entry.PlayerLimit != 0 && len(options.Participants) > int(entry.PlayerLimit) {
		return Config{}, fmt.Errorf("competitive map %d supports at most %d participants, got %d", entry.ID, entry.PlayerLimit, len(options.Participants))
	}
	grid, err := validateNativeRuntimeMap(entry)
	if err != nil {
		return Config{}, err
	}
	spec, ok := mapdata.LookupCompetitiveRuleByNativeID(entry.NativeRule)
	if !ok || spec.RoundDurationMS == 0 {
		return Config{}, fmt.Errorf("competitive map %d has no native rule duration", entry.ID)
	}
	rules := options.Rules
	rules.StartClockMS = NativeRoundStartClockMS
	rules.RoundDurationMS = spec.RoundDurationMS
	rules.BombFuseMS = NativeBombFuseMS
	rules.FlameDurationMS = NativeFlameDurationMS
	rules.ActorHalfSizePixels = NativeActorHalfSizePixels
	rules.SpeedPixelsPerSecondByRate = nativeSpeedPixelsPerSecondByRate
	participants, err := PlaceNativeSpawns(options.SpawnSeed, options.SpawnMode, competitiveCells(entry.SpawnGroupA), competitiveCells(entry.SpawnGroupB), options.Participants)
	if err != nil {
		return Config{}, fmt.Errorf("competitive map %d: %w", entry.ID, err)
	}
	var items, delayed []mapdata.CompetitiveWallItem
	if options.UseRecordedWallItems {
		// This legacy flag selects the recorded GAME_BEGIN arrays together.
		// An old recording with no Items must not acquire today's new supply.
		items, delayed = options.RecordedWallItems, options.RecordedDelayedItems
	} else {
		roles := make([]uint16, len(options.Participants))
		for i, p := range options.Participants {
			roles[i] = p.RoleID
		}
		supply := entry.PlanCompetitiveSupply(options.ItemSeed, roles, false)
		items, delayed = supply.WallItems, supply.DelayedItems
	}
	pickups, err := HiddenPickupsFromCompetitiveWallItems(entry, options.ItemSeed, items)
	if err != nil {
		return Config{}, err
	}
	scheduled, err := ScheduledPickupsFromItems(options.ItemSeed, delayed)
	if err != nil {
		return Config{}, err
	}
	seed := options.SimulationSeed
	if seed == 0 {
		seed = uint64(options.SpawnSeed)<<32 | uint64(options.ItemSeed)
	}
	return Config{
		Seed: seed, Grid: grid, Rules: rules, Participants: participants, Pickups: pickups,
		ScheduledPickups:      scheduled,
		PublicWallItemProfile: publicWallItemProfileFromWire(entry, items),
	}, nil
}

// GAME_BEGIN broadcasts final quantities to every participant. Expose only
// those public counts, never ItemSeed's hidden coordinates or future schedule.
func publicWallItemProfileFromWire(entry mapdata.CompetitiveMap, items []mapdata.CompetitiveWallItem) PublicWallItemProfile {
	var profile PublicWallItemProfile
	if len(entry.HiddenItemCells) == 0 {
		return profile
	}
	for _, item := range items {
		if category, ok := publicWallItemCategory(item.SceneID); ok && item.Quantity > 0 {
			profile.Categories[category].PresenceProbability = 1
			profile.Categories[category].ExpectedDensity += float32(item.Quantity) / float32(len(entry.HiddenItemCells))
		}
	}
	for i := range profile.Categories {
		profile.Categories[i].ExpectedDensity = min(1, profile.Categories[i].ExpectedDensity)
	}
	return profile
}

func publicWallItemProfile(entry mapdata.CompetitiveMap) PublicWallItemProfile {
	var result PublicWallItemProfile
	if len(entry.HiddenItemCells) == 0 {
		return result
	}
	noneProbability := [PublicWallItemCategoryCount]float32{}
	for index := range noneProbability {
		noneProbability[index] = 1
	}
	capacity := float32(len(entry.HiddenItemCells))
	for _, rule := range entry.WallItemRules {
		if entry.SuppressCompetitiveWallItem(rule.SceneID) {
			continue
		}
		category, ok := publicWallItemCategory(rule.SceneID)
		if !ok || rule.Probability <= 0 || rule.Maximum <= 0 {
			continue
		}
		probability := rule.Probability
		if probability > 1 {
			probability = 1
		}
		minimum := rule.Minimum
		if minimum < 1 {
			minimum = 1
		}
		maximum := rule.Maximum
		if maximum < minimum {
			continue
		}
		noneProbability[category] *= 1 - probability
		expected := probability * float32(int32(minimum)+int32(maximum)) / 2
		result.Categories[category].ExpectedDensity += expected / capacity
	}
	for category := range result.Categories {
		result.Categories[category].PresenceProbability = 1 - noneProbability[category]
		if result.Categories[category].ExpectedDensity > 1 {
			result.Categories[category].ExpectedDensity = 1
		}
	}
	return result
}

func publicWallItemCategory(sceneID uint32) (PublicWallItemCategory, bool) {
	switch sceneID {
	case SceneBombCapacitySmall, SceneBombCapacityLarge:
		return WallItemBombCapacity, true
	case SceneBombPowerSmall, SceneBombPowerLarge:
		return WallItemBombPower, true
	case SceneSpeedSmall, SceneSpeedLarge, SceneFastMovement:
		return WallItemMovement, true
	case SceneHiddenPickupReach:
		return WallItemDetector, true
	}
	if _, ok := sceneelement.NativeBattleActionPickup(sceneelement.ID(sceneID)); ok {
		return WallItemBattleAction, true
	}
	if _, ok := sceneelement.NativeTransformation(sceneelement.ID(sceneID)); ok {
		return WallItemTransformation, true
	}
	if reward, ok := sceneelement.NativeReward(sceneelement.ID(sceneID)); ok && reward.DirectMatchAccumulator {
		return WallItemReward, true
	}
	if _, _, ok := supportedPickupEffect(sceneID); ok {
		return WallItemUtility, true
	}
	return 0, false
}

// HiddenPickupsFromCompetitiveMap combines the server-owned quantity roll with
// the client's native ItemSeed coordinate shuffle over the exact primary list
// serialized in the selected .map file.
func HiddenPickupsFromCompetitiveMap(entry mapdata.CompetitiveMap, itemSeed uint32, participantCount int) ([]Pickup, error) {
	if entry.NativeRule != 1 || entry.Rule != mapdata.CompetitiveRuleOrdinary {
		return nil, fmt.Errorf("competitive map %d uses native rule %d/%s, want ordinary rule 1", entry.ID, entry.NativeRule, entry.Rule)
	}
	if err := validateSupportedWallItemRules(entry); err != nil {
		return nil, err
	}
	capacity := len(entry.HiddenItemCells)
	// Recorded live/replay lists bypass this branch because GAME_BEGIN already
	// contains the server's final quantities.
	items := entry.RollFinalCompetitiveOrdinaryWallItems(
		itemSeed, participantCount, capacity, capacity/2,
	)
	return HiddenPickupsFromCompetitiveWallItems(entry, itemSeed, items)
}

// HiddenPickupsFromCompetitiveWallItems applies the client's native ItemSeed
// coordinate shuffle to an already-authoritative GAME_BEGIN.NewItems list.
// Differential replay uses this boundary so a historical server roll is not
// silently replaced by the current local policy.
func HiddenPickupsFromCompetitiveWallItems(entry mapdata.CompetitiveMap, itemSeed uint32, items []mapdata.CompetitiveWallItem) ([]Pickup, error) {
	if entry.NativeRule != 1 || entry.Rule != mapdata.CompetitiveRuleOrdinary {
		return nil, fmt.Errorf("competitive map %d uses native rule %d/%s, want ordinary rule 1", entry.ID, entry.NativeRule, entry.Rule)
	}
	for index, item := range items {
		if item.SceneID == 0 || item.Quantity <= 0 {
			return nil, fmt.Errorf("competitive map %d recorded wall item %d has invalid scene/quantity %d/%d", entry.ID, index, item.SceneID, item.Quantity)
		}
		if _, _, supported := supportedPickupEffect(item.SceneID); !supported {
			return nil, fmt.Errorf("competitive map %d recorded wall item %d uses unsupported scene ID %d", entry.ID, index, item.SceneID)
		}
	}
	return PlaceHiddenPickups(itemSeed, competitiveCells(entry.HiddenItemCells), items)
}

func competitiveCells(cells []mapdata.CompetitiveCell) []Cell {
	result := make([]Cell, len(cells))
	for index, cell := range cells {
		result[index] = Cell{Row: int16(cell.Row), Col: int16(cell.Col)}
	}
	return result
}
