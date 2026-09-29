package mapdata

import (
	"crypto/md5"
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"qqtang/internal/clientdata/sceneelement"
)

func TestLoadCatalogSelectionAndNextStage(t *testing.T) {
	root := t.TempDir()
	for _, directory := range []string{"map", "config", filepath.Join("object", "mapElem")} {
		if err := os.MkdirAll(filepath.Join(root, directory), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	mapElements := "class QQTMapElem1(QQTMapElem):\n    imageID = 1\n    GridAttr = (0,)\n"
	if err := os.WriteFile(filepath.Join(root, "object", "mapElem", "mapElem.py"), []byte(mapElements), 0o600); err != nil {
		t.Fatal(err)
	}
	description := "(1601, 'pve', 'one', 15, 13, 4, 'pve01.map', 'x', 'x', 0, '', 1, 3),\n" +
		"(1602, 'pve', 'one', 15, 13, 4, 'pve02.map', 'x', 'x', 0, '', 1, 0),\n" +
		"(1612, 'pve', 'two', 15, 13, 4, 'pve12.map', 'x', 'x', 0, '', 1, 3),\n" +
		"(1, 'water', 'normal', 15, 13, 4, 'water01.map', 'x', 'x', 200, '', 1, 0),\n" +
		"(16, 'water', 'item', 15, 13, 8, 'water12.map', 'x', 'x', 0, '', 1, 1),\n" +
		"(701, 'bomb', 'special', 15, 13, 8, 'bomb01.map', 'x', 'x', 0, '', 1, 0),\n"
	if err := os.WriteFile(filepath.Join(root, "map", "mapDesc.py"), []byte(description), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, id := range []int{1601, 1602, 1612} {
		name := fmt.Sprintf("pve%02d.map", id-1600)
		if err := os.WriteFile(filepath.Join(root, "map", name), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "config", fmt.Sprintf("battlefield_map%d.dat", id)), []byte{byte(id)}, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for name, rule := range map[string]uint32{"water01.map": 1, "water12.map": 1, "bomb01.map": 2} {
		wallRules := []CompetitiveWallItemRule{{SceneID: 1, Minimum: 8, Maximum: 8, Probability: 1}}
		if name == "water12.map" {
			wallRules = append(wallRules, CompetitiveWallItemRule{SceneID: 23, Minimum: 1, Maximum: 1, Probability: 1})
		}
		contents := testCompetitiveMapBytes(rule, wallRules)
		if err := os.WriteFile(filepath.Join(root, "map", name), contents, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	continueINI := "[public]\ncontinuenum=2\n[continue1]\nmaps=1601,1602\n[continue2]\nmaps=1612\n"
	if err := os.WriteFile(filepath.Join(root, "config", "Continue.ini"), []byte(continueINI), 0o600); err != nil {
		t.Fatal(err)
	}
	catalog, err := LoadCatalog(root)
	if err != nil {
		t.Fatal(err)
	}
	first, ok := catalog.SelectableMap(1601)
	if !ok || first.SequenceID != 1 || first.MapIndex != 0 {
		t.Fatalf("first map = %+v, %v", first, ok)
	}
	if _, ok := catalog.SelectableMap(1602); ok {
		t.Fatal("a continuation stage must not be directly selectable")
	}
	next, ok := catalog.Next(1601)
	if !ok || next.ID != 1602 || next.MapIndex != 1 {
		t.Fatalf("next map = %+v, %v", next, ok)
	}
	if _, ok := catalog.Next(1602); ok {
		t.Fatal("last stage unexpectedly has a successor")
	}
	wantHash := fmt.Sprintf("%X", md5.Sum([]byte("pve01.map")))
	if string(first.MapHash[:]) != wantHash {
		t.Fatalf("map hash = %q, want %q", first.MapHash, wantHash)
	}
	ordinary, err := catalog.RandomOrdinaryCompetitive(4, 0)
	if err != nil || ordinary.ID != 1 || ordinary.Name != "normal" || ordinary.RequiredPoints != 200 || !ordinary.UsesOrdinaryElimination() {
		t.Fatalf("ordinary competitive map = %+v, %v", ordinary, err)
	}
	if _, err = catalog.RandomEligibleOrdinaryCompetitive(4, 0, 199, false); err == nil {
		t.Fatal("points-gated random selection unexpectedly succeeded")
	}
	if selected, selectErr := catalog.RandomEligibleOrdinaryCompetitive(4, 0, 0, true); selectErr != nil || selected.ID != 1 {
		t.Fatalf("VIP random selection = %+v, %v", selected, selectErr)
	}
	itemMap, err := catalog.RandomOrdinaryCompetitive(8, 1)
	if err != nil || itemMap.ID != 16 || itemMap.RequiredItemField != 1 ||
		len(itemMap.WallItemRules) != 1 || itemMap.WallItemRules[0].SceneID != 1 ||
		itemMap.WallItemSource != CompetitiveWallItemsItemFieldFiltered {
		t.Fatalf("item competitive map = %+v, %v", itemMap, err)
	}
	special, ok := catalog.CompetitiveMap(701)
	if !ok || special.NativeRule != 2 || special.Rule != CompetitiveRuleKickBomb || special.UsesOrdinaryElimination() || !special.UsesServerElimination() {
		t.Fatalf("special competitive map = %+v, %v", special, ok)
	}
	rolled := ordinary.RollWallItems(0x12345678, 4, 8, 4)
	if len(rolled) != 1 || rolled[0] != (CompetitiveWallItem{SceneID: 1, Quantity: 8}) {
		t.Fatalf("ordinary competitive wall items = %+v", rolled)
	}
	if len(ordinary.AirborneCells) != competitiveMapWidthV3*competitiveMapHeightV3 {
		t.Fatalf("ordinary competitive airborne cells = %d", len(ordinary.AirborneCells))
	}
}

func TestRollWallItemsGuaranteesBaseProgressionWithinWallCapacity(t *testing.T) {
	entry := CompetitiveMap{
		ID: 15,
		WallItemRules: []CompetitiveWallItemRule{
			{SceneID: 1, Minimum: 8, Maximum: 10, Probability: 0.5},
			{SceneID: 2, Minimum: 8, Maximum: 10, Probability: 0.25},
			{SceneID: 3, Minimum: 8, Maximum: 10, Probability: 0.75},
			{SceneID: 104, Minimum: 5, Maximum: 5, Probability: 1},
		},
	}
	items := entry.RollWallItems(0x12345678, 4, 30, 15)
	quantities := make(map[uint32]int16, len(items))
	total := 0
	for _, item := range items {
		quantities[item.SceneID] += item.Quantity
		total += int(item.Quantity)
	}
	if quantities[1] < 5 || quantities[2] < 5 || quantities[3] < 5 {
		t.Fatalf("base floors = %+v, want SceneID 1/2/3 >= 5/5/5", quantities)
	}
	if total > 30 {
		t.Fatalf("rolled quantity = %d, exceeds hidden-wall capacity 30: %+v", total, items)
	}
	for index := 0; index < 3; index++ {
		if index >= len(items) || items[index].SceneID != uint32(index+1) {
			t.Fatalf("base-first wall item order = %+v", items)
		}
	}
}

func TestRollWallItemsSinglePlayerFloorIsFour(t *testing.T) {
	entry := CompetitiveMap{
		ID: 15,
		WallItemRules: []CompetitiveWallItemRule{
			{SceneID: 1, Minimum: 8, Maximum: 20, Probability: 0.5},
		},
	}
	items := entry.RollWallItems(1, 1, 64, 32)
	if len(items) != 1 || items[0].SceneID != 1 || items[0].Quantity < 4 {
		t.Fatalf("single-player large-map floor = %+v, want at least four", items)
	}
}

func TestRollWallItemsTinyMapBalancesNativeRollAtPhysicalCapacity(t *testing.T) {
	entry := CompetitiveMap{
		ID: 15,
		WallItemRules: []CompetitiveWallItemRule{
			{SceneID: 1, Minimum: 20, Maximum: 20, Probability: 1},
			{SceneID: 2, Minimum: 20, Maximum: 20, Probability: 1},
			{SceneID: 3, Minimum: 20, Maximum: 20, Probability: 1},
			{SceneID: 104, Minimum: 5, Maximum: 5, Probability: 1},
		},
	}
	items := entry.RollWallItems(1, 8, 5, 2)
	total := 0
	for _, item := range items {
		total += int(item.Quantity)
	}
	if total != 5 {
		t.Fatalf("tiny-map roll = %+v, want all five physical wall cells used", items)
	}
	want := []CompetitiveWallItem{{SceneID: 1, Quantity: 2}, {SceneID: 2, Quantity: 2}, {SceneID: 3, Quantity: 1}}
	if !slices.Equal(items, want) {
		t.Fatalf("tiny-map base allocation = %+v, want level-filled %+v", items, want)
	}
}

func TestBaseGuaranteeBudgetUsesPriorityOnlyToBreakEqualQuantityTie(t *testing.T) {
	rules := []CompetitiveWallItemRule{{SceneID: 1}, {SceneID: 2}, {SceneID: 3}}
	got := allocateBaseWallItemQuantities(rules, []int16{7, 7, 7}, 2)
	want := []int16{1, 1, 0}
	if !slices.Equal(got, want) {
		t.Fatalf("two-cell guarantee = %v, want %v", got, want)
	}
}

func TestBaseGuaranteeRebalancesNativeQuantityBeforeAddingFloor(t *testing.T) {
	rules := []CompetitiveWallItemRule{{SceneID: 1}, {SceneID: 2}, {SceneID: 3}}
	got := rebalanceBaseWallItemQuantities(rules, []int16{10, 0, 0}, []int16{5, 5, 5})
	want := []int16{4, 3, 3}
	if !slices.Equal(got, want) {
		t.Fatalf("rebalanced 10/0/0 = %v, want %v", got, want)
	}
	if got[0]+got[1]+got[2] != 10 {
		t.Fatalf("rebalance changed native total: %v", got)
	}

	got = rebalanceBaseWallItemQuantities(rules, []int16{20, 0, 0}, []int16{5, 5, 5})
	want = []int16{10, 5, 5}
	if !slices.Equal(got, want) {
		t.Fatalf("rebalanced 20/0/0 = %v, want %v", got, want)
	}

	got = rebalanceBaseWallItemQuantities(rules, []int16{5, 4, 4}, []int16{5, 5, 5})
	want = []int16{5, 4, 4}
	if !slices.Equal(got, want) {
		t.Fatalf("difference-one boundary = %v, want no high/low inversion from %v", got, want)
	}
}

func TestBaseGuaranteeAppliesHalfWallToAllTypesOnlyWhenDeficitRemains(t *testing.T) {
	rules := []CompetitiveWallItemRule{{SceneID: 1}, {SceneID: 2}, {SceneID: 3}}
	floors := []int16{5, 5, 5}

	got := finalizeBaseWallItemQuantities(rules, []int16{4, 3, 3}, floors, 30, 15)
	want := []int16{5, 5, 5}
	if !slices.Equal(got, want) {
		t.Fatalf("supplement-before-compression result = %v, want %v", got, want)
	}

	got = finalizeBaseWallItemQuantities(rules, []int16{5, 4, 4}, floors, 30, 12)
	want = []int16{4, 4, 4}
	if !slices.Equal(got, want) {
		t.Fatalf("remaining-deficit half-wall result = %v, want %v", got, want)
	}

	got = finalizeBaseWallItemQuantities(rules, []int16{20, 0, 0}, floors, 30, 12)
	want = []int16{10, 5, 5}
	if !slices.Equal(got, want) {
		t.Fatalf("fully supplied native result = %v, want %v without half-wall truncation", got, want)
	}
}

func TestRollWallItemsReducesHighestBaseQuantityBeforeFloorCandidates(t *testing.T) {
	entry := CompetitiveMap{
		ID: 15,
		WallItemRules: []CompetitiveWallItemRule{
			{SceneID: 1, Minimum: 10, Maximum: 10, Probability: 1},
			{SceneID: 2, Minimum: 4, Maximum: 4, Probability: 1},
			{SceneID: 3, Minimum: 4, Maximum: 4, Probability: 1},
		},
	}
	items := entry.RollWallItems(1, 2, 12, 6)
	quantities := make(map[uint32]int16, len(items))
	for _, item := range items {
		quantities[item.SceneID] += item.Quantity
	}
	if quantities[1] != 4 || quantities[2] != 4 || quantities[3] != 4 {
		t.Fatalf("capacity-balanced base roll = %+v, want 4/4/4 instead of starving lower quantities", items)
	}
}

func TestRollWallItemsPreservesNativeBaseRollAboveGuaranteeBudget(t *testing.T) {
	entry := CompetitiveMap{
		ID: 15,
		WallItemRules: []CompetitiveWallItemRule{
			{SceneID: 1, Minimum: 10, Maximum: 10, Probability: 1},
			{SceneID: 104, Minimum: 1, Maximum: 1, Probability: 1},
		},
	}
	items := entry.RollWallItems(1, 2, 12, 6)
	quantities := make(map[uint32]int16, len(items))
	for _, item := range items {
		quantities[item.SceneID] += item.Quantity
	}
	if quantities[1] != 10 {
		t.Fatalf("native base roll = %+v, want all 10 SceneID 1 items despite six-cell guarantee budget", items)
	}
	if quantities[104] != 1 {
		t.Fatalf("remaining native reward = %+v, want SceneID 104 preserved", items)
	}
}

func TestCompensatedWallItemProbabilityIsDampedAndNeverForcesDrop(t *testing.T) {
	got := compensatedWallItemProbability(0.5, 16, 8)
	want := 1 - math.Pow(0.5, math.Sqrt(2))
	if math.Abs(got-want) > 0.000001 {
		t.Fatalf("half-capacity probability = %.9f, want %.9f", got, want)
	}
	if rare := compensatedWallItemProbability(0.01, 16, 8); rare <= 0.01 || rare >= 0.02 {
		t.Fatalf("rare compensated probability = %.9f, want damped increase below linear doubling", rare)
	}
	if high := compensatedWallItemProbability(0.8, 16, 8); high <= 0.8 || high >= 1 {
		t.Fatalf("high compensated probability = %.9f, want increase without forced drop", high)
	}
}

func TestRollWallItemsDoesNotInventUndefinedBaseTypes(t *testing.T) {
	entry := CompetitiveMap{
		ID: 15,
		WallItemRules: []CompetitiveWallItemRule{
			{SceneID: 1, Minimum: 8, Maximum: 8, Probability: 0},
			{SceneID: 104, Minimum: 1, Maximum: 1, Probability: 1},
		},
	}
	items := entry.RollWallItems(7, 8, 20, 10)
	if len(items) != 1 || items[0] != (CompetitiveWallItem{SceneID: 104, Quantity: 1}) {
		t.Fatalf("zero-probability base rule was incorrectly guaranteed: %+v", items)
	}
}

func TestShippedWallItemFloorNeverExceedsNativeHiddenCells(t *testing.T) {
	root := filepath.Join("..", "..", "..", "runtime", "client-patched")
	if _, err := os.Stat(filepath.Join(root, "map", "mapDesc.py")); os.IsNotExist(err) {
		t.Skip("verified runtime client is not present")
	}
	catalog, err := LoadCatalog(root)
	if err != nil {
		t.Fatal(err)
	}
	seeds := []uint32{1, 0x12345678, 0x89ABCDEF, 0xFFFFFFFF}
	for _, mapID := range catalog.AllCompetitiveIDs() {
		entry, ok := catalog.CompetitiveMapMetadata(mapID)
		if !ok || entry.RequiredItemField != 0 || len(entry.WallItemRules) == 0 {
			continue
		}
		for _, players := range []int{1, 2, 4, 8} {
			if entry.PlayerLimit != 0 && players > int(entry.PlayerLimit) {
				continue
			}
			for _, seed := range seeds {
				capacity := len(entry.HiddenItemCells)
				items := entry.RollWallItems(seed, players, capacity, capacity/2)
				total := 0
				quantities := make(map[uint32]int)
				for _, item := range items {
					total += int(item.Quantity)
					quantities[item.SceneID] += int(item.Quantity)
				}
				if total > len(entry.HiddenItemCells) {
					t.Fatalf("map %d players %d seed %08X rolls %d items into %d hidden cells", mapID, players, seed, total, len(entry.HiddenItemCells))
				}
				baseFloorTotal := 0
				for _, rule := range entry.WallItemRules {
					if rule.SceneID >= 1 && rule.SceneID <= 3 && rule.Probability > 0 && rule.Maximum > 0 {
						floor := 3 + (players+1)/2
						if floor > int(rule.Maximum) {
							floor = int(rule.Maximum)
						}
						baseFloorTotal += floor
					}
				}
				if capacity/2 >= baseFloorTotal && baseFloorTotal != 0 {
					for _, rule := range entry.WallItemRules {
						if rule.SceneID < 1 || rule.SceneID > 3 || rule.Probability <= 0 || rule.Maximum <= 0 {
							continue
						}
						floor := 3 + (players+1)/2
						if floor > int(rule.Maximum) {
							floor = int(rule.Maximum)
						}
						if quantities[rule.SceneID] < floor {
							t.Fatalf("map %d players %d seed %08X has SceneID %d quantity %d below floor %d: %+v", mapID, players, seed, rule.SceneID, quantities[rule.SceneID], floor, items)
						}
					}
				}
			}
		}
	}
}

func TestShippedItemFieldsFilterOnlyNativeActionSlotPickups(t *testing.T) {
	root := filepath.Join("..", "..", "..", "runtime", "client-patched")
	if _, err := os.Stat(filepath.Join(root, "map", "mapDesc.py")); os.IsNotExist(err) {
		t.Skip("verified runtime client is not present")
	}
	catalog, err := LoadCatalog(root)
	if err != nil {
		t.Fatal(err)
	}

	for _, mapID := range catalog.AllCompetitiveIDs() {
		entry, ok := catalog.CompetitiveMapMetadata(mapID)
		if !ok || entry.RequiredItemField != 1 {
			continue
		}
		if entry.WallItemSource != CompetitiveWallItemsItemFieldFiltered && entry.WallItemSource != CompetitiveWallItemsItemFieldRecovery {
			t.Fatalf("item-field map %d source = %q", mapID, entry.WallItemSource)
		}
		if len(entry.WallItemRules) == 0 {
			t.Fatalf("item-field map %d has no contact-pickup wall pool", mapID)
		}
		if entry.WallItemSource == CompetitiveWallItemsItemFieldRecovery && entry.WallItemDonorMapID == 0 {
			t.Fatalf("recovered item-field map %d has no donor provenance", mapID)
		}
		for _, rule := range entry.WallItemRules {
			if _, actionPickup := sceneelement.NativeBattleActionPickup(sceneelement.ID(rule.SceneID)); actionPickup {
				t.Fatalf("item-field map %d retained action-slot pickup SceneID %d", mapID, rule.SceneID)
			}
		}
	}
	for _, mapID := range []uint32{514, 519, 520, 521} {
		entry, ok := catalog.CompetitiveMapMetadata(mapID)
		if !ok || entry.RequiredItemField != 1 {
			t.Fatalf("shipped ice item-field map %d = %+v/%t", mapID, entry, ok)
		}
		base := map[uint32]bool{}
		for _, rule := range entry.WallItemRules {
			if rule.SceneID >= 1 && rule.SceneID <= 3 && rule.Probability > 0 {
				base[rule.SceneID] = true
			}
		}
		if !base[1] || !base[2] || !base[3] {
			t.Fatalf("ice item-field map %d base progression = %v, rules %+v", mapID, base, entry.WallItemRules)
		}
	}
}

func TestItemFieldWallRecoveryUsesCompatibleOriginalPoolAndFiltersActions(t *testing.T) {
	catalog := &Catalog{competitiveMaps: map[uint32]CompetitiveMap{
		10: {
			ID: 10, Family: "snow", NativeRule: 1, WallItemSource: CompetitiveWallItemsEmbedded,
			WallItemRules: []CompetitiveWallItemRule{
				{SceneID: 1, Minimum: 8, Maximum: 8, Probability: 1},
				{SceneID: 23, Minimum: 1, Maximum: 1, Probability: 1},
			},
		},
		14: {ID: 14, Family: "snow", NativeRule: 1, RequiredItemField: 1, WallItemSource: CompetitiveWallItemsItemFieldFiltered},
		15: {ID: 15, Family: "snow", NativeRule: 2, WallItemSource: CompetitiveWallItemsEmbedded, WallItemRules: []CompetitiveWallItemRule{{SceneID: 2, Minimum: 1, Maximum: 1, Probability: 1}}},
	}}
	catalog.restoreCompetitiveWallItemRules()
	got := catalog.competitiveMaps[14]
	if got.WallItemSource != CompetitiveWallItemsItemFieldRecovery || got.WallItemDonorMapID != 10 {
		t.Fatalf("item-field recovery provenance = %q/%d", got.WallItemSource, got.WallItemDonorMapID)
	}
	if len(got.WallItemRules) != 1 || got.WallItemRules[0].SceneID != 1 {
		t.Fatalf("item-field recovered rules = %+v, want contact pickup only", got.WallItemRules)
	}
}

func TestShippedMap908IgnoresOrphanNegativeEditorMarker(t *testing.T) {
	root := filepath.Join("..", "..", "..", "runtime", "client-patched")
	if _, err := os.Stat(filepath.Join(root, "map", "mapDesc.py")); os.IsNotExist(err) {
		t.Skip("verified runtime client is not present")
	}
	catalog, err := LoadCatalog(root)
	if err != nil {
		t.Fatal(err)
	}
	entry, ok := catalog.CompetitiveMapMetadata(908)
	if !ok {
		t.Fatal("shipped competitive map 908 is missing")
	}
	cell, inside := entry.Battlefield.Cell(12, 8)
	if !inside || cell.MapElementOccupied || cell.Collision != CompetitiveCellOpen {
		t.Fatalf("map 908 cell 12,8 = %+v/%t, want native runtime opening", cell, inside)
	}
	foundAirborne := false
	for _, candidate := range entry.AirborneCells {
		if candidate.Row == 12 && candidate.Col == 8 {
			foundAirborne = true
			break
		}
	}
	if !foundAirborne {
		t.Fatal("map 908 native runtime opening 12,8 is absent from dispatch candidates")
	}
}

func TestParseCompetitiveAirborneCellsUsesRuntimeMapElementProjection(t *testing.T) {
	field := CompetitiveBattlefield{
		Width: competitiveMapWidthV3, Height: competitiveMapHeightV3,
		Cells: make([]CompetitiveBattleCell, competitiveMapWidthV3*competitiveMapHeightV3),
	}
	// Runtime occupancy already includes every configured positive-anchor
	// footprint cell and deliberately excludes orphan negative editor markers.
	field.Cells[2*competitiveMapWidthV3+3].MapElementOccupied = true
	field.Cells[4*competitiveMapWidthV3+5].MapElementOccupied = true
	cells, err := parseCompetitiveAirborneCells(field)
	if err != nil {
		t.Fatal(err)
	}
	if len(cells) != competitiveMapWidthV3*competitiveMapHeightV3-2 {
		t.Fatalf("airborne cells = %d", len(cells))
	}
	for _, cell := range cells {
		if (cell.Row == 2 && cell.Col == 3) || (cell.Row == 4 && cell.Col == 5) {
			t.Fatalf("occupied cell survived: %+v", cell)
		}
	}
}

func TestParseCompetitiveBattlefieldUsesNativeGridAttributes(t *testing.T) {
	elementPath := filepath.Join(t.TempDir(), "mapElem.py")
	elements := "class QQTMapElem10(QQTMapElem):\n" +
		"    LifeTime = 1\n    canMove = 2\n    imageID = 10\n    GridAttr = (0,)\n" +
		"class QQTMapElem11(QQTMapElem):\n" +
		"    LifeTime = 2\n    imageID = 11\n    GridAttr = (0,)\n" +
		"class QQTMapElem20(QQTMapElem):\n" +
		"    imageID = 20\n    GridAttr = (0,)\n" +
		"class QQTMapElem30(QQTMapElem):\n" +
		"    imageID = 30\n    GridAttr = (5,)\n" +
		"class QQTMapElem40(QQTMapElem):\n" +
		"    imageID = 40\n    size = (2, 1)\n    GridAttr = (0, 5)\n" +
		"class QQTMapElem50(QQTMapElem):\n" +
		"    imageID = 50\n    GridAttr = (4,)\n" +
		"class QQTMapElem60(QQTMapElem):\n" +
		"    imageID = 60\n    GridAttr = (1,)\n"
	if err := os.WriteFile(elementPath, []byte(elements), 0o600); err != nil {
		t.Fatal(err)
	}
	contents := testCompetitiveMapBytes(1, nil)
	put := func(row, col int, value int32) {
		offset := 12 + (row*competitiveMapWidthV3+col)*4
		binary.LittleEndian.PutUint32(contents[offset:offset+4], uint32(value))
	}
	put(1, 1, 10)
	put(1, 2, 20)
	put(1, 3, 40)
	put(1, 4, -40)
	put(1, 5, 30)
	put(1, 6, 999)
	put(1, 7, 50)
	put(1, 8, 60)
	put(1, 9, 11)
	put(1, 10, -20) // orphan continuation marker: Client.exe creates no object

	elementCatalog, err := loadCompetitiveMapElements(elementPath)
	if err != nil {
		t.Fatal(err)
	}
	field, err := parseCompetitiveBattlefield(elementCatalog, contents)
	if err != nil {
		t.Fatal(err)
	}
	want := map[int]CompetitiveBattleCell{
		0:  {Collision: CompetitiveCellOpen, FlamePassable: true},
		1:  {Collision: CompetitiveCellBreakable, MapElementOccupied: true, Durability: 1, MapElementID: 10, NormalPushable: true, PandaPushable: true, ElementWidth: 1, ElementHeight: 1, ElementAnchorRow: 1, ElementAnchorCol: 1},
		2:  {Collision: CompetitiveCellSolid, MapElementOccupied: true},
		3:  {Collision: CompetitiveCellSolid, MapElementOccupied: true},
		4:  {Collision: CompetitiveCellOpen, MapElementOccupied: true},
		5:  {Collision: CompetitiveCellOpen, MapElementOccupied: true},
		6:  {Collision: CompetitiveCellSolid, MapElementOccupied: true},
		7:  {Collision: CompetitiveCellOpen, MapElementOccupied: true},
		8:  {Collision: CompetitiveCellOpen, MapElementOccupied: true},
		9:  {Collision: CompetitiveCellBreakable, MapElementOccupied: true, Durability: 2, MapElementID: 11, PandaPushable: true, ElementWidth: 1, ElementHeight: 1, ElementAnchorRow: 1, ElementAnchorCol: 9},
		10: {Collision: CompetitiveCellOpen, FlamePassable: true},
	}
	attrs := map[int]uint32{1: 0, 2: 0, 3: 0, 4: 5, 5: 5, 7: 4, 8: 1, 9: 0}
	for col, expected := range want {
		expected.NativePlayerExitAttrSet = true
		expected.NativePlayerExitAttr = 15
		if attr, exists := attrs[col]; exists {
			expected.NativeGridAttr, expected.NativeGridAttrSet = attr, true
			expected.NativePlayerExitAttr = attr
		}
		if col == 6 {
			expected.NativePlayerExitAttr = 0
		} // unknown positive first-layer object
		got, ok := field.Cell(1, col)
		if !ok || got != expected {
			t.Fatalf("battlefield cell 1,%d = %+v/%t, want %+v", col, got, ok, expected)
		}
	}
}

func testCompetitiveMapBytes(rule uint32, itemRules []CompetitiveWallItemRule) []byte {
	const tableOffset = 12 + competitiveMapTileLayers*competitiveMapWidthV3*competitiveMapHeightV3*4
	contents := make([]byte, tableOffset+4+len(itemRules)*16+16)
	binary.LittleEndian.PutUint32(contents[0:4], 3)
	binary.LittleEndian.PutUint32(contents[4:8], rule)
	binary.LittleEndian.PutUint32(contents[8:12], 8)
	binary.LittleEndian.PutUint32(contents[tableOffset:tableOffset+4], uint32(len(itemRules)))
	offset := tableOffset + 4
	for _, itemRule := range itemRules {
		binary.LittleEndian.PutUint32(contents[offset:offset+4], itemRule.SceneID)
		binary.LittleEndian.PutUint32(contents[offset+4:offset+8], uint32(itemRule.Minimum))
		binary.LittleEndian.PutUint32(contents[offset+8:offset+12], uint32(itemRule.Maximum))
		binary.LittleEndian.PutUint32(contents[offset+12:offset+16], math.Float32bits(itemRule.Probability))
		offset += 16
	}
	return contents
}

func TestCompetitiveRuleRegistryCarriesPlayerLifecycle(t *testing.T) {
	want := map[uint32]CompetitivePlayerLifecycle{
		1:  CompetitiveLifecyclePermanentElimination,
		2:  CompetitiveLifecyclePermanentElimination,
		3:  CompetitiveLifecycleTimedRespawn,
		4:  CompetitiveLifecycleTimedRespawn,
		5:  CompetitiveLifecycleTimedRespawn,
		6:  CompetitiveLifecycleTimedRespawn,
		7:  CompetitiveLifecycleNativeDurability,
		8:  CompetitiveLifecycleNativeDurability,
		13: CompetitiveLifecycleTimedRespawn,
	}
	for nativeID, lifecycle := range want {
		spec, ok := LookupCompetitiveRuleByNativeID(nativeID)
		if !ok || spec.PlayerLifecycle != lifecycle {
			t.Fatalf("native rule %d lifecycle = %q/%v, want %q", nativeID, spec.PlayerLifecycle, ok, lifecycle)
		}
	}
}

func TestCompetitiveObjectiveSceneItemsUseNativeRulePools(t *testing.T) {
	treasure := CompetitiveMap{Rule: CompetitiveRuleTreasure}
	items := treasure.CompetitiveObjectiveSceneItems(2, 1)
	wantTreasure := []CompetitiveWallItem{
		{SceneID: 150, Quantity: 16},
		{SceneID: 151, Quantity: 8},
		{SceneID: 152, Quantity: 4},
	}
	if fmt.Sprint(items) != fmt.Sprint(wantTreasure) {
		t.Fatalf("2-member treasure team scene pool = %+v, want %+v", items, wantTreasure)
	}
	weighted := 0
	for _, item := range items {
		switch item.SceneID {
		case 150:
			weighted += int(item.Quantity)
		case 151:
			weighted += 2 * int(item.Quantity)
		case 152:
			weighted += 3 * int(item.Quantity)
		}
	}
	if weighted != 44 {
		t.Fatalf("2-member treasure team scene value = %d, want 44", weighted)
	}

	sculpture := CompetitiveMap{Rule: CompetitiveRuleSculpture}
	for seed, special := range []uint32{162, 163, 164} {
		wantSculpture := []CompetitiveWallItem{
			{SceneID: 161, Quantity: 6},
			{SceneID: special, Quantity: 1},
		}
		if got := sculpture.CompetitiveObjectiveSceneItems(1, uint32(seed)); fmt.Sprint(got) != fmt.Sprint(wantSculpture) {
			t.Fatalf("sculpture scene pool seed %d = %+v, want %+v", seed, got, wantSculpture)
		}
	}
	if got := (CompetitiveMap{Rule: CompetitiveRuleOrdinary}).CompetitiveObjectiveSceneItems(1, 1); got != nil {
		t.Fatalf("ordinary map objective scene pool = %+v, want nil", got)
	}
	if got := treasure.CompetitiveObjectiveSceneItems(0, 1); got != nil {
		t.Fatalf("zero-player treasure scene pool = %+v, want nil", got)
	}

	wantTank := []CompetitiveWallItem{
		{SceneID: 201, Quantity: 2},
		{SceneID: 203, Quantity: 2},
		{SceneID: 204, Quantity: 2},
	}
	if got := (CompetitiveMap{Rule: CompetitiveRuleTank}).CompetitiveObjectiveSceneItems(2, 1); fmt.Sprint(got) != fmt.Sprint(wantTank) {
		t.Fatalf("tank objective scene pool = %+v, want %+v", got, wantTank)
	}
}

func TestCompetitiveBossCatalogCoversNamedFinalClientEncounters(t *testing.T) {
	want := map[uint32][]struct {
		id     string
		roleID uint16
	}{
		11: {{"sailor", 32}}, 124: {{"sailor", 32}},
		411: {{"first_mate", 33}}, 104: {{"first_mate", 33}},
		12: {{"hook", 34}}, 16: {{"hook", 34}},
		905: {{"thief_griffin", 24}, {"christmas_griffin", 28}},
		906: {{"thief_griffin", 24}, {"christmas_griffin", 28}},
		910: {{"thief_griffin", 24}, {"christmas_griffin", 28}},
		211: {{"thief_griffin", 24}, {"christmas_griffin", 28}, {"ghost_griffin", 30}, {"little_nian", 29}, {"great_nian", 31}},
		212: {{"thief_griffin", 24}, {"christmas_griffin", 28}, {"ghost_griffin", 30}, {"little_nian", 29}, {"great_nian", 31}},
		102: {{"thief_griffin", 24}}, 510: {{"christmas_griffin", 28}},
		511: {{"ghost_griffin", 30}}, 512: {{"little_nian", 29}}, 513: {{"great_nian", 31}},
		702: {{"cristiano", 35}}, 701: {{"rooney", 36}},
	}
	for mapID, expected := range want {
		got := LookupCompetitiveBossCandidates(mapID)
		if len(got) != len(expected) {
			t.Fatalf("map %d Boss candidates = %+v, want %d", mapID, got, len(expected))
		}
		for index, candidate := range got {
			if candidate.ID != expected[index].id || candidate.Entity.RoleID != expected[index].roleID ||
				candidate.Entity.HP == 0 || candidate.Overlay.Kind != CompetitiveOverlayBoss ||
				candidate.Overlay.TeamProjection != CompetitiveTeamsUnified {
				t.Fatalf("map %d Boss candidate[%d] = %+v, want %s role %d", mapID, index, candidate, expected[index].id, expected[index].roleID)
			}
		}
	}

	firstMate := LookupCompetitiveBossCandidates(411)[0]
	if len(firstMate.ItemOptions) != 1 || len(firstMate.ItemOptions[0].Requirements) != 1 ||
		firstMate.ItemOptions[0].Requirements[0] != (CompetitiveBossItemRequirement{ItemID: 460, Count: 1}) {
		t.Fatalf("First Mate summon requirement = %+v", firstMate.ItemOptions)
	}
	firstMate.ItemOptions[0].Requirements[0].Count = 999
	if LookupCompetitiveBossCandidates(411)[0].ItemOptions[0].Requirements[0].Count != 1 {
		t.Fatal("Boss candidate lookup leaked mutable item requirements")
	}
	for _, expected := range []struct {
		id     string
		roleID uint16
		hp     uint16
	}{
		{"sailor", 32, 5}, {"first_mate", 33, 10}, {"hook", 34, 10},
		{"thief_griffin", 24, 5}, {"christmas_griffin", 28, 10}, {"ghost_griffin", 30, 10},
		{"little_nian", 29, 5}, {"great_nian", 31, 10}, {"cristiano", 35, 5}, {"rooney", 36, 10},
	} {
		candidate, ok := LookupCompetitiveBossCandidate(expected.id)
		if !ok || candidate.Entity.RoleID != expected.roleID || candidate.Entity.HP != expected.hp {
			t.Fatalf("Boss lookup %q = %+v/%t", expected.id, candidate, ok)
		}
	}
	if _, ok := LookupCompetitiveBossCandidate("unreleased_guess"); ok {
		t.Fatal("unknown Boss candidate was accepted")
	}

	for _, testCase := range []struct {
		id           string
		wantAirborne bool
	}{
		{"first_mate", false},
		{"hook", false},
		// Football maps already install the native rule-2 producer. Marking the
		// overlay would schedule a second server-authored 0x10E1 stream.
		{"cristiano", false},
		{"rooney", false},
	} {
		candidate, ok := LookupCompetitiveBossCandidate(testCase.id)
		if !ok {
			t.Fatalf("Boss lookup %q failed", testCase.id)
		}
		hasAirborne := false
		for _, capability := range candidate.Overlay.SceneCapabilities {
			if capability == CompetitiveBossSceneAirborneBombs {
				hasAirborne = true
			}
		}
		if hasAirborne != testCase.wantAirborne {
			t.Fatalf("Boss %q airborne capability = %t, want %t", testCase.id, hasAirborne, testCase.wantAirborne)
		}
	}
}

func TestCompetitiveBossCombatProfilesMatchNativeRoleRows(t *testing.T) {
	maximumWire := CompetitiveBossCombatTuple{8, 8, 9}
	want := map[string]CompetitiveBossCombatProfile{
		"sailor":            {NativeInitial: CompetitiveBossCombatTuple{7, 3, 4}, NativeMaximum: CompetitiveBossCombatTuple{8, 8, 9}, Wire: maximumWire},
		"first_mate":        {NativeInitial: CompetitiveBossCombatTuple{7, 0, 4}, NativeMaximum: CompetitiveBossCombatTuple{8, 0, 9}, Wire: maximumWire},
		"hook":              {NativeInitial: CompetitiveBossCombatTuple{7, 0, 4}, NativeMaximum: CompetitiveBossCombatTuple{8, 0, 9}, Wire: maximumWire},
		"thief_griffin":     {NativeInitial: CompetitiveBossCombatTuple{7, 3, 4}, NativeMaximum: CompetitiveBossCombatTuple{8, 8, 9}, Wire: maximumWire},
		"christmas_griffin": {NativeInitial: CompetitiveBossCombatTuple{7, 5, 6}, NativeMaximum: CompetitiveBossCombatTuple{8, 8, 9}, Wire: maximumWire},
		"ghost_griffin":     {NativeInitial: CompetitiveBossCombatTuple{7, 5, 6}, NativeMaximum: CompetitiveBossCombatTuple{8, 8, 9}, Wire: maximumWire},
		"little_nian":       {NativeInitial: CompetitiveBossCombatTuple{7, 3, 4}, NativeMaximum: CompetitiveBossCombatTuple{8, 8, 9}, Wire: maximumWire},
		"great_nian":        {NativeInitial: CompetitiveBossCombatTuple{7, 5, 6}, NativeMaximum: CompetitiveBossCombatTuple{8, 8, 9}, Wire: maximumWire},
		"cristiano":         {NativeInitial: CompetitiveBossCombatTuple{7, 0, 4}, NativeMaximum: CompetitiveBossCombatTuple{8, 0, 9}, Wire: maximumWire},
		"rooney":            {NativeInitial: CompetitiveBossCombatTuple{7, 0, 4}, NativeMaximum: CompetitiveBossCombatTuple{8, 0, 9}, Wire: maximumWire},
	}
	for id, expected := range want {
		candidate, ok := LookupCompetitiveBossCandidate(id)
		if !ok {
			t.Fatalf("Boss lookup %q failed", id)
		}
		profile, ok := candidate.Entity.NativeCombatProfile()
		if !ok || profile != expected {
			t.Fatalf("Boss %q native combat profile = %+v/%t, want %+v", id, profile, ok, expected)
		}
		if candidate.Entity.Rate != expected.Wire.Rate || candidate.Entity.Bubble != expected.Wire.Bubble || candidate.Entity.Power != expected.Wire.Power {
			t.Fatalf("Boss %q wire combat tuple = %d/%d/%d, want %+v", id, candidate.Entity.Rate, candidate.Entity.Bubble, candidate.Entity.Power, expected.Wire)
		}
	}
}

func TestCompetitiveBossMapsDoNotOverlapScoredNativeObjectives(t *testing.T) {
	root := filepath.Join("..", "..", "..", "runtime", "client-patched")
	if _, err := os.Stat(filepath.Join(root, "map", "mapDesc.py")); os.IsNotExist(err) {
		t.Skip("verified runtime client is not present")
	}
	catalog, err := LoadCatalog(root)
	if err != nil {
		t.Fatal(err)
	}
	for mapID := range competitiveBossCandidates {
		entry, ok := catalog.CompetitiveMap(mapID)
		if !ok {
			t.Fatalf("Boss map %d is absent from final-client catalog", mapID)
		}
		if entry.Rule != CompetitiveRuleOrdinary && entry.Rule != CompetitiveRuleKickBomb {
			t.Fatalf("Boss map %d uses scored native objective %s; settlement precedence must be designed before enabling it", mapID, entry.Rule)
		}
	}
}

func TestShippedClientAdventureCatalog(t *testing.T) {
	root := filepath.Join("..", "..", "..", "runtime", "client-patched")
	if _, err := os.Stat(filepath.Join(root, "map", "mapDesc.py")); os.IsNotExist(err) {
		t.Skip("verified runtime client is not present")
	}
	catalog, err := LoadCatalog(root)
	if err != nil {
		t.Fatal(err)
	}
	wantStarts := []uint32{1601, 1607, 1612, 1616, 1623, 1628, 1633, 1637, 1640, 1643, 1646, 1649}
	gotStarts := catalog.SelectableIDs()
	if fmt.Sprint(gotStarts) != fmt.Sprint(wantStarts) {
		t.Fatalf("selectable adventure maps = %v, want %v", gotStarts, wantStarts)
	}
	first, ok := catalog.SelectableMap(1601)
	if !ok || string(first.MapHash[:]) != "AD8C5668C199C5521616A7D2D1829DDC" {
		t.Fatalf("shipped map 1601 = %+v, %v", first, ok)
	}
	current := first
	for _, want := range []uint32{1602, 1603, 1604, 1605, 1606} {
		current, ok = catalog.Next(current.ID)
		if !ok || current.ID != want {
			t.Fatalf("next stage after %d = %+v, %v; want %d", current.ID, current, ok, want)
		}
	}
	if _, ok := catalog.Next(1606); ok {
		t.Fatal("Magic Kingdom 1 final stage unexpectedly has a successor")
	}
	normal, ok := catalog.CompetitiveMap(1)
	if !ok || normal.Family != "water" || normal.Name != "水面01" || normal.PlayerLimit != 4 || normal.RequiredItemField != 0 || normal.NativeRule != 1 || normal.Rule != CompetitiveRuleOrdinary {
		t.Fatalf("shipped competitive map 1 = %+v, %v", normal, ok)
	}
	water11, ok := catalog.CompetitiveMap(11)
	if !ok || water11.NativeRule != 1 || water11.Rule != CompetitiveRuleOrdinary || !water11.UsesOrdinaryElimination() ||
		len(water11.BossCandidates) != 1 || water11.BossCandidates[0].ID != "sailor" ||
		water11.BossCandidates[0].Activation != CompetitiveBossActivationUnconditional ||
		water11.BossCandidates[0].Overlay.Kind != CompetitiveOverlayBoss ||
		water11.BossCandidates[0].Overlay.TeamProjection != CompetitiveTeamsUnified ||
		water11.BossCandidates[0].Overlay.UnifiedTeamID != 1 ||
		water11.BossCandidates[0].Overlay.BossTemplate != CompetitiveBossTemplateSharedNative ||
		len(water11.WallItemRules) != 16 {
		t.Fatalf("shipped Water11 Boss candidates = %+v, %v", water11, ok)
	}
	if water11.WallItemRules[0] != (CompetitiveWallItemRule{SceneID: 1, Minimum: 8, Maximum: 8, Probability: 1}) {
		t.Fatalf("shipped Water11 recovered wall-item rules = %+v", water11.WallItemRules)
	}
	wantTransformations := map[uint32]CompetitiveWallItemRule{
		104: {SceneID: 104, Minimum: 0, Maximum: 1, Probability: 0.5},
		110: {SceneID: 110, Minimum: 0, Maximum: 1, Probability: 0.8},
		114: {SceneID: 114, Minimum: 0, Maximum: 1, Probability: 1},
	}
	for _, rule := range water11.WallItemRules {
		if want, exists := wantTransformations[rule.SceneID]; exists {
			if rule != want {
				t.Fatalf("Water11 transformation rule %d = %+v, want %+v", rule.SceneID, rule, want)
			}
			delete(wantTransformations, rule.SceneID)
		}
	}
	if len(wantTransformations) != 0 {
		t.Fatalf("Water11 is missing server transformation rules: %+v", wantTransformations)
	}
	if water11.WallItemSource != CompetitiveWallItemsFamilyRecovery || water11.WallItemDonorMapID != 10 {
		t.Fatalf("Water11 wall-item provenance = %s/%d", water11.WallItemSource, water11.WallItemDonorMapID)
	}
	bun01, ok := catalog.CompetitiveMap(801)
	if !ok || len(bun01.WallItemRules) == 0 || bun01.WallItemSource != CompetitiveWallItemsFamilyRecovery || bun01.WallItemDonorMapID != 802 {
		t.Fatalf("Bun01 recovered wall items = %+v, %v", bun01, ok)
	}
	hero01, ok := catalog.CompetitiveMap(1201)
	if !ok || hero01.WallItemSource != CompetitiveWallItemsEmbedded || len(hero01.WallItemRules) == 0 {
		t.Fatalf("Hero Legend 01 donor = %+v, %v", hero01, ok)
	}
	for mapID := uint32(1701); mapID <= 1708; mapID++ {
		tank, found := catalog.CompetitiveMap(mapID)
		if !found || tank.Rule != CompetitiveRuleTank || tank.WallItemSource != CompetitiveWallItemsExplicitDonor ||
			tank.WallItemDonorMapID != 1201 || fmt.Sprint(tank.WallItemRules) != fmt.Sprint(hero01.WallItemRules) {
			t.Fatalf("tank map %d recovered wall items = %+v, found %v", mapID, tank, found)
		}
		for _, rule := range tank.WallItemRules {
			if rule.SceneID == 104 || rule.SceneID == 110 || rule.SceneID == 114 {
				t.Fatalf("tank map %d inherited forbidden transformation %d", mapID, rule.SceneID)
			}
		}
	}
	wrestleRule, ok := LookupCompetitiveRuleByNativeID(4)
	if !ok || wrestleRule.RoundDurationMS != 180_000 {
		t.Fatalf("wrestle native duration = %+v, %v", wrestleRule, ok)
	}
	pig, ok := catalog.CompetitiveMap(901)
	if !ok || pig.Family != "pig" || pig.NativeRule != 1 || !pig.UsesOrdinaryElimination() {
		t.Fatalf("shipped pig map 901 = %+v, %v", pig, ok)
	}
}
