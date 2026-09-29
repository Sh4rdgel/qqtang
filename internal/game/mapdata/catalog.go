package mapdata

import (
	"bufio"
	"crypto/md5"
	cryptorand "crypto/rand"
	"encoding/binary"
	"fmt"
	"math"
	"math/big"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"qqtang/internal/clientdata/sceneelement"

	"golang.org/x/text/encoding/simplifiedchinese"
)

var (
	adventureMapLine = regexp.MustCompile(`(?m)^\(\s*([0-9]+),\s*'pve',\s*'[^']*',\s*[0-9]+,\s*[0-9]+,\s*[0-9]+,\s*'([^']+\.map)'`)
	clientMapLine    = regexp.MustCompile(`(?m)^\(\s*([0-9]+),\s*'([^']+)',\s*'([^']*)',\s*[0-9]+,\s*[0-9]+,\s*([0-9]+),\s*'([^']+\.map)',\s*'[^']*',\s*'[^']*',\s*([0-9]+),\s*'[^']*'\s*,\s*([01])\s*,\s*([0-9]+)\s*\),`)
)

// CompetitiveRuleKind is the native battlefield rule family selected by the
// client map catalog. It is intentionally independent from room GameType
// (no-item/item/loot field) and settlement GameMode.
type CompetitiveRuleKind string

// CompetitiveObjective identifies the battlefield condition owned by one
// native rule object. It is intentionally independent from the result-table
// field ordinal: several modes reuse the fourth GAME_OVER field with
// different presentation labels.
type CompetitiveObjective string

const (
	CompetitiveObjectiveElimination CompetitiveObjective = "elimination"
	CompetitiveObjectiveKickBomb    CompetitiveObjective = "kick_bomb"
	CompetitiveObjectiveBun         CompetitiveObjective = "bun_deposit"
	CompetitiveObjectiveWrestle     CompetitiveObjective = "wrestle"
	CompetitiveObjectiveTreasure    CompetitiveObjective = "treasure"
	CompetitiveObjectiveSculpture   CompetitiveObjective = "sculpture"
	CompetitiveObjectiveMachine     CompetitiveObjective = "machine"
	CompetitiveObjectiveBox         CompetitiveObjective = "box"
	CompetitiveObjectiveTankBase    CompetitiveObjective = "tank_base"
)

// CompetitiveConclusionAuthority describes which side can prove that the
// objective has finished. Ordinary elimination is reconstructed by the
// server from trapped/kill/death events. Special native rules currently send
// their complete result table from the elected arbitrator; the server still
// validates membership and persists the settlement.
type CompetitiveConclusionAuthority string

const (
	CompetitiveConclusionServerElimination CompetitiveConclusionAuthority = "server_elimination"
	CompetitiveConclusionNativeArbitrator  CompetitiveConclusionAuthority = "native_arbitrator"
)

// CompetitivePlayerLifecycle describes what one native rule does after an
// avatar is defeated. This is independent from the match conclusion owner:
// treasure and bun rounds are still concluded by the native arbitrator, but
// defeated players respawn and continue participating in the same round.
type CompetitivePlayerLifecycle string

const (
	CompetitiveLifecyclePermanentElimination CompetitivePlayerLifecycle = "permanent_elimination"
	CompetitiveLifecycleTimedRespawn         CompetitivePlayerLifecycle = "timed_respawn"
	CompetitiveLifecycleNativeDurability     CompetitivePlayerLifecycle = "native_durability"
)

// CompetitiveEventFamily names a confirmed protocol family used by a rule.
// Keeping this semantic (rather than importing protocol schema constants)
// avoids a mapdata/protocol dependency cycle while still making coverage
// machine-readable.
type CompetitiveEventFamily string

const (
	CompetitiveEventCombat      CompetitiveEventFamily = "combat"
	CompetitiveEventKickBomb    CompetitiveEventFamily = "kick_bomb"
	CompetitiveEventBun         CompetitiveEventFamily = "bun"
	CompetitiveEventWrestle     CompetitiveEventFamily = "wrestle"
	CompetitiveEventTreasure    CompetitiveEventFamily = "treasure"
	CompetitiveEventSculpture   CompetitiveEventFamily = "sculpture"
	CompetitiveEventMachineBomb CompetitiveEventFamily = "machine_bomb"
	CompetitiveEventBox         CompetitiveEventFamily = "box"
	CompetitiveEventTankBase    CompetitiveEventFamily = "tank_base"
	CompetitiveEventBoss        CompetitiveEventFamily = "boss_overlay"
)

// CompetitiveRuleSpec is the single authoritative registry entry for one
// native rule ID. Do not add map-family switches in server handlers; lookup
// this contract and attach only genuine per-map overlays separately.
type CompetitiveRuleSpec struct {
	NativeID  uint32               `json:"native_id"`
	Kind      CompetitiveRuleKind  `json:"kind"`
	Objective CompetitiveObjective `json:"objective"`
	// RoundDurationMS is returned by slot 9 of the native rule vtable. The
	// client countdown reads this value directly; GAME_BEGIN.GameTime is not
	// the universal round duration.
	RoundDurationMS      uint32                         `json:"round_duration_ms"`
	ConclusionAuthority  CompetitiveConclusionAuthority `json:"conclusion_authority"`
	PlayerLifecycle      CompetitivePlayerLifecycle     `json:"player_lifecycle"`
	NativePlayerHitLimit byte                           `json:"native_player_hit_limit,omitempty"`
	EventFamilies        []CompetitiveEventFamily       `json:"event_families"`
}

const (
	CompetitiveRuleUnknown   CompetitiveRuleKind = "unknown"
	CompetitiveRuleOrdinary  CompetitiveRuleKind = "ordinary"
	CompetitiveRuleKickBomb  CompetitiveRuleKind = "kick_bomb"
	CompetitiveRuleBun       CompetitiveRuleKind = "bun"
	CompetitiveRuleWrestle   CompetitiveRuleKind = "wrestle"
	CompetitiveRuleTreasure  CompetitiveRuleKind = "treasure"
	CompetitiveRuleSculpture CompetitiveRuleKind = "sculpture"
	CompetitiveRuleMachine   CompetitiveRuleKind = "machine"
	CompetitiveRuleBox       CompetitiveRuleKind = "box"
	CompetitiveRuleTank      CompetitiveRuleKind = "tank"
)

var competitiveRuleSpecs = [...]CompetitiveRuleSpec{
	{
		NativeID: 1, Kind: CompetitiveRuleOrdinary, Objective: CompetitiveObjectiveElimination,
		RoundDurationMS:     240_000,
		ConclusionAuthority: CompetitiveConclusionServerElimination, PlayerLifecycle: CompetitiveLifecyclePermanentElimination,
		EventFamilies: []CompetitiveEventFamily{CompetitiveEventCombat, CompetitiveEventBoss},
	},
	{
		NativeID: 2, Kind: CompetitiveRuleKickBomb, Objective: CompetitiveObjectiveKickBomb,
		RoundDurationMS:     240_000,
		ConclusionAuthority: CompetitiveConclusionServerElimination, PlayerLifecycle: CompetitiveLifecyclePermanentElimination,
		EventFamilies: []CompetitiveEventFamily{CompetitiveEventCombat, CompetitiveEventKickBomb},
	},
	{
		NativeID: 3, Kind: CompetitiveRuleBun, Objective: CompetitiveObjectiveBun,
		RoundDurationMS:     240_000,
		ConclusionAuthority: CompetitiveConclusionNativeArbitrator, PlayerLifecycle: CompetitiveLifecycleTimedRespawn,
		EventFamilies: []CompetitiveEventFamily{CompetitiveEventCombat, CompetitiveEventBun},
	},
	{
		NativeID: 4, Kind: CompetitiveRuleWrestle, Objective: CompetitiveObjectiveWrestle,
		RoundDurationMS:     180_000,
		ConclusionAuthority: CompetitiveConclusionNativeArbitrator, PlayerLifecycle: CompetitiveLifecycleTimedRespawn,
		EventFamilies: []CompetitiveEventFamily{CompetitiveEventCombat, CompetitiveEventWrestle},
	},
	{
		NativeID: 5, Kind: CompetitiveRuleTreasure, Objective: CompetitiveObjectiveTreasure,
		RoundDurationMS:     180_000,
		ConclusionAuthority: CompetitiveConclusionNativeArbitrator, PlayerLifecycle: CompetitiveLifecycleTimedRespawn,
		EventFamilies: []CompetitiveEventFamily{CompetitiveEventCombat, CompetitiveEventTreasure},
	},
	{
		NativeID: 6, Kind: CompetitiveRuleSculpture, Objective: CompetitiveObjectiveSculpture,
		RoundDurationMS:     240_000,
		ConclusionAuthority: CompetitiveConclusionNativeArbitrator, PlayerLifecycle: CompetitiveLifecycleTimedRespawn,
		EventFamilies: []CompetitiveEventFamily{CompetitiveEventCombat, CompetitiveEventSculpture},
	},
	{
		NativeID: 7, Kind: CompetitiveRuleMachine, Objective: CompetitiveObjectiveMachine,
		RoundDurationMS:     240_000,
		ConclusionAuthority: CompetitiveConclusionNativeArbitrator, PlayerLifecycle: CompetitiveLifecycleNativeDurability,
		NativePlayerHitLimit: 4,
		EventFamilies:        []CompetitiveEventFamily{CompetitiveEventCombat, CompetitiveEventMachineBomb},
	},
	{
		NativeID: 8, Kind: CompetitiveRuleBox, Objective: CompetitiveObjectiveBox,
		RoundDurationMS:     240_000,
		ConclusionAuthority: CompetitiveConclusionNativeArbitrator, PlayerLifecycle: CompetitiveLifecycleNativeDurability,
		NativePlayerHitLimit: 4,
		EventFamilies:        []CompetitiveEventFamily{CompetitiveEventCombat, CompetitiveEventBox},
	},
	{
		NativeID: 13, Kind: CompetitiveRuleTank, Objective: CompetitiveObjectiveTankBase,
		RoundDurationMS:     240_000,
		ConclusionAuthority: CompetitiveConclusionNativeArbitrator, PlayerLifecycle: CompetitiveLifecycleTimedRespawn,
		EventFamilies: []CompetitiveEventFamily{CompetitiveEventCombat, CompetitiveEventTankBase},
	},
}

func LookupCompetitiveRuleByNativeID(id uint32) (CompetitiveRuleSpec, bool) {
	for _, spec := range competitiveRuleSpecs {
		if spec.NativeID == id {
			spec.EventFamilies = append([]CompetitiveEventFamily(nil), spec.EventFamilies...)
			return spec, true
		}
	}
	return CompetitiveRuleSpec{}, false
}

func LookupCompetitiveRule(kind CompetitiveRuleKind) (CompetitiveRuleSpec, bool) {
	for _, spec := range competitiveRuleSpecs {
		if spec.Kind == kind {
			spec.EventFamilies = append([]CompetitiveEventFamily(nil), spec.EventFamilies...)
			return spec, true
		}
	}
	return CompetitiveRuleSpec{}, false
}

func CompetitiveRuleSpecs() []CompetitiveRuleSpec {
	result := make([]CompetitiveRuleSpec, 0, len(competitiveRuleSpecs))
	for _, spec := range competitiveRuleSpecs {
		spec.EventFamilies = append([]CompetitiveEventFamily(nil), spec.EventFamilies...)
		result = append(result, spec)
	}
	return result
}

// competitiveRuleForNativeID maps the rule selector stored in bytes 4..7 of
// every original .map file. Client.exe passes this value directly to its
// battlefield-rule factory. Directory family names are presentation/filter
// metadata and are not authoritative (notably, pig maps use native rule 1).
func competitiveRuleForNativeID(id uint32) CompetitiveRuleKind {
	if spec, ok := LookupCompetitiveRuleByNativeID(id); ok {
		return spec.Kind
	}
	return CompetitiveRuleUnknown
}

// AdventureMap is one client-owned PVE stage. MapHash is the 32-byte uppercase
// ASCII MD5 string consumed by QQTSection. SequenceID identifies the locally
// installed route in Continue.ini; it is not the GAME_BEGIN remote-download
// continuation identifier.
type AdventureMap struct {
	ID         uint32
	FileName   string
	MapHash    [32]byte
	SequenceID int32
	MapIndex   int
}

// CompetitiveMap is one locally installed non-PVE battlefield. Family is the
// mapDesc.py presentation/filter family (for example water, bomb, or bun).
// NativeRule, read from the map file itself, is the authoritative gameplay
// selector. RequiredItemField is mapDesc's final "is item map" flag: 0 means
// the no-item/treasure lobby, while 1 means the item lobby. Item-lobby maps
// suppress wall pickups that enter the action shortcut slots, because those
// come from the player's carried competitive inventory. Contact pickups such
// as base attributes, transformations and rewards remain native wall drops.
type CompetitiveMap struct {
	ID                 uint32
	Family             string
	Name               string
	FileName           string
	MapHash            [32]byte
	PlayerLimit        byte
	Selectable         bool
	RequiredPoints     uint32
	RequiredItemField  byte
	NativeRule         uint32
	Rule               CompetitiveRuleKind
	BossCandidates     []CompetitiveBossCandidate
	WallItemRules      []CompetitiveWallItemRule
	WallItemSource     CompetitiveWallItemSource
	WallItemDonorMapID uint32
	// HiddenItemCells, SpawnGroupA and SpawnGroupB are serialized after the
	// wall-item rules in the native .map file. HiddenItemCells is the exact
	// primary coordinate list consumed by the client's ItemSeed shuffle;
	// SpawnGroupA/B are the two source vectors consumed by the native player
	// placement routines. They must not be reconstructed from collision cells.
	HiddenItemCells []CompetitiveCell
	SpawnGroupA     []CompetitiveCell
	SpawnGroupB     []CompetitiveCell
	// ObjectiveCells is the fourth native coordinate table. It belongs to
	// special-rule objects (bases/statues/etc.) and is retained as map evidence,
	// but the restricted rule-1 engine deliberately does not consume it.
	ObjectiveCells []CompetitiveCell
	// Battlefield is the collision projection recovered from the first two
	// native tile layers and object/mapElem/mapElem.py. It keeps only actor
	// collision, flame traversal and wall durability needed by the restricted
	// standard-battle engine; presentation and special-rule objects remain
	// client-owned.
	Battlefield CompetitiveBattlefield
	// AirborneCells are statically empty cells recovered from the first two
	// native tile layers. Runtime producers additionally reject cells occupied
	// by live scene objects; this immutable pool is the server-side starting
	// point for rule overlays that author NOTIFY_DISPATCH_BOMB.
	AirborneCells []CompetitiveCell
}

// CompetitiveWallItemSource records why GAME_BEGIN contains (or deliberately
// omits) a wall-item pool. Embedded map tables remain authoritative. For an
// item field, ItemFieldFiltered removes only native action-slot pickups while
// preserving contact pickups. ItemFieldRecovery fills an empty filtered item
// field from an original table with the same family and native rule, then
// applies that same action-slot filter. FamilyRecovery does the corresponding
// recovery for a non-item map. ExplicitDonor is reserved for a rule whose
// installed maps all lost their table but whose original behaviour and a
// concrete compatible donor are both known.
type CompetitiveWallItemSource string

const (
	CompetitiveWallItemsNone              CompetitiveWallItemSource = "none"
	CompetitiveWallItemsEmbedded          CompetitiveWallItemSource = "embedded"
	CompetitiveWallItemsFamilyRecovery    CompetitiveWallItemSource = "family_recovery"
	CompetitiveWallItemsExplicitDonor     CompetitiveWallItemSource = "explicit_donor"
	CompetitiveWallItemsItemFieldFiltered CompetitiveWallItemSource = "item_field_filtered"
	CompetitiveWallItemsItemFieldRecovery CompetitiveWallItemSource = "item_field_recovery"
)

type CompetitiveCell struct {
	Row byte
	Col byte
}

// CompetitiveWallItemRule is the original per-map wall-item record embedded
// after the three tile layers in QQTang's .map format. Probability gates the
// record; after a successful roll the generated quantity is in [Minimum,
// Maximum]. A zero Minimum means that the item is optional, not that a
// successful roll should emit a zero-quantity GAME_ITEM_TYPE.
type CompetitiveWallItemRule struct {
	SceneID     uint32  `json:"scene_id"`
	Minimum     int16   `json:"minimum"`
	Maximum     int16   `json:"maximum"`
	Probability float32 `json:"probability"`
}

type CompetitiveWallItem struct {
	SceneID  uint32
	Quantity int16
}

func (entry CompetitiveMap) UsesOrdinaryElimination() bool {
	return entry.Rule == CompetitiveRuleOrdinary
}

func (entry CompetitiveMap) UsesServerElimination() bool {
	spec, ok := LookupCompetitiveRule(entry.Rule)
	return ok && spec.ConclusionAuthority == CompetitiveConclusionServerElimination
}

// Catalog is built exclusively from the verified local client resources. A
// selectable map is the first stage in a Continue.ini sequence; later stages
// are reached through REQUEST_GAME_NEXTMAP.
type Catalog struct {
	maps            map[uint32]AdventureMap
	competitiveMaps map[uint32]CompetitiveMap
	sequences       map[int32][]uint32
}

func LoadCatalog(clientRoot string) (*Catalog, error) {
	if clientRoot == "" {
		return nil, fmt.Errorf("client root is empty")
	}
	mapDirectory := filepath.Join(clientRoot, "map")
	description, err := os.ReadFile(filepath.Join(mapDirectory, "mapDesc.py"))
	if err != nil {
		return nil, fmt.Errorf("read mapDesc.py: %w", err)
	}
	description, err = simplifiedchinese.GBK.NewDecoder().Bytes(description)
	if err != nil {
		return nil, fmt.Errorf("decode mapDesc.py as GBK: %w", err)
	}
	catalog := &Catalog{
		maps: make(map[uint32]AdventureMap), competitiveMaps: make(map[uint32]CompetitiveMap),
		sequences: make(map[int32][]uint32),
	}
	competitiveElements, err := loadCompetitiveMapElements(filepath.Join(clientRoot, "object", "mapElem", "mapElem.py"))
	if err != nil {
		return nil, err
	}
	for _, fields := range clientMapLine.FindAllSubmatch(description, -1) {
		parsed, parseErr := strconv.ParseUint(string(fields[1]), 10, 32)
		if parseErr != nil {
			return nil, fmt.Errorf("parse client map ID %q: %w", fields[1], parseErr)
		}
		family := string(fields[2])
		if parsed == 0 || family == "rand" || family == "pve" || family == "practice" {
			continue
		}
		playerLimit, parseErr := strconv.ParseUint(string(fields[4]), 10, 8)
		if parseErr != nil || playerLimit == 0 || playerLimit > 8 {
			return nil, fmt.Errorf("client map %d player limit %q is outside 1..8", parsed, fields[4])
		}
		fileName := string(fields[5])
		metadata, hashErr := loadCompetitiveMapMetadata(mapDirectory, competitiveElements, uint32(parsed), fileName)
		if hashErr != nil {
			return nil, hashErr
		}
		requiredPoints, parseErr := strconv.ParseUint(string(fields[6]), 10, 32)
		if parseErr != nil {
			return nil, fmt.Errorf("parse client map %d required points %q: %w", parsed, fields[6], parseErr)
		}
		itemField, parseErr := strconv.ParseUint(string(fields[8]), 10, 8)
		if parseErr != nil {
			return nil, fmt.Errorf("parse client map %d item-field flag %q: %w", parsed, fields[8], parseErr)
		}
		wallItemSource := CompetitiveWallItemsNone
		wallItemRules := metadata.wallItemRules
		if itemField == 1 {
			// 道具场 obtains action-slot consumables from the player's carried
			// inventory. Its embedded wall table is nevertheless authoritative
			// for contact pickups (base attributes, transformations and rewards).
			wallItemRules = filterItemFieldWallItemRules(wallItemRules)
			wallItemSource = CompetitiveWallItemsItemFieldFiltered
		} else if len(wallItemRules) != 0 {
			wallItemSource = CompetitiveWallItemsEmbedded
		}
		catalog.competitiveMaps[uint32(parsed)] = CompetitiveMap{
			ID: uint32(parsed), Family: family, Name: string(fields[3]), FileName: fileName, MapHash: metadata.mapHash,
			PlayerLimit: byte(playerLimit), Selectable: string(fields[7]) == "1", RequiredPoints: uint32(requiredPoints), RequiredItemField: byte(itemField),
			NativeRule: metadata.nativeRule, Rule: competitiveRuleForNativeID(metadata.nativeRule),
			BossCandidates: LookupCompetitiveBossCandidates(uint32(parsed)), WallItemRules: wallItemRules,
			WallItemSource: wallItemSource, HiddenItemCells: metadata.hiddenItemCells,
			SpawnGroupA: metadata.spawnGroupA, SpawnGroupB: metadata.spawnGroupB, ObjectiveCells: metadata.objectiveCells,
			AirborneCells: metadata.airborneCells, Battlefield: metadata.battlefield,
		}
	}
	catalog.restoreCompetitiveWallItemRules()
	for _, match := range adventureMapLine.FindAllSubmatch(description, -1) {
		parsed, parseErr := strconv.ParseUint(string(match[1]), 10, 32)
		if parseErr != nil {
			return nil, fmt.Errorf("parse adventure map ID %q: %w", match[1], parseErr)
		}
		fileName := string(match[2])
		if filepath.Base(fileName) != fileName {
			return nil, fmt.Errorf("adventure map %d has unsafe filename %q", parsed, fileName)
		}
		mapHash, hashErr := loadMapHash(mapDirectory, uint32(parsed), fileName)
		if hashErr != nil {
			return nil, hashErr
		}
		entry := AdventureMap{ID: uint32(parsed), FileName: fileName, SequenceID: -1, MapIndex: -1}
		entry.MapHash = mapHash
		catalog.maps[entry.ID] = entry
	}
	if len(catalog.maps) == 0 {
		return nil, fmt.Errorf("mapDesc.py contains no PVE maps")
	}
	if err := catalog.loadSequences(filepath.Join(clientRoot, "config", "Continue.ini")); err != nil {
		return nil, err
	}
	return catalog, nil
}

func filterItemFieldWallItemRules(rules []CompetitiveWallItemRule) []CompetitiveWallItemRule {
	filtered := make([]CompetitiveWallItemRule, 0, len(rules))
	for _, rule := range rules {
		if _, actionPickup := sceneelement.NativeBattleActionPickup(sceneelement.ID(rule.SceneID)); actionPickup {
			continue
		}
		filtered = append(filtered, rule)
	}
	return filtered
}

type competitiveMapMetadata struct {
	mapHash         [32]byte
	nativeRule      uint32
	wallItemRules   []CompetitiveWallItemRule
	hiddenItemCells []CompetitiveCell
	spawnGroupA     []CompetitiveCell
	spawnGroupB     []CompetitiveCell
	objectiveCells  []CompetitiveCell
	airborneCells   []CompetitiveCell
	battlefield     CompetitiveBattlefield
}

func loadCompetitiveMapMetadata(mapDirectory string, elements map[uint32]competitiveMapElement, mapID uint32, fileName string) (competitiveMapMetadata, error) {
	var metadata competitiveMapMetadata
	if filepath.Base(fileName) != fileName {
		return metadata, fmt.Errorf("competitive map %d has unsafe filename %q", mapID, fileName)
	}
	mapBytes, err := os.ReadFile(filepath.Join(mapDirectory, fileName))
	if err != nil {
		return metadata, fmt.Errorf("read competitive map %d %s: %w", mapID, fileName, err)
	}
	copy(metadata.mapHash[:], fmt.Sprintf("%X", md5.Sum(mapBytes)))
	if len(mapBytes) < 12 {
		return metadata, fmt.Errorf("competitive map %d %s header is truncated", mapID, fileName)
	}
	metadata.nativeRule = binary.LittleEndian.Uint32(mapBytes[4:8])
	if metadata.nativeRule == 0 {
		return metadata, fmt.Errorf("competitive map %d %s has zero native rule", mapID, fileName)
	}
	itemTableOffset, err := competitiveWallItemTableOffset(mapBytes)
	if err != nil {
		return metadata, fmt.Errorf("competitive map %d %s: %w", mapID, fileName, err)
	}
	metadata.wallItemRules, itemTableOffset, err = parseCompetitiveWallItemRules(mapBytes, itemTableOffset)
	if err != nil {
		return metadata, fmt.Errorf("competitive map %d %s: %w", mapID, fileName, err)
	}
	_, width, height, err := competitiveMapGeometry(mapBytes)
	if err != nil {
		return metadata, fmt.Errorf("competitive map %d %s: %w", mapID, fileName, err)
	}
	metadata.hiddenItemCells, itemTableOffset, err = parseCompetitiveCellTable(mapBytes, itemTableOffset, width, height, uint64(width)*uint64(height), "hidden-item")
	if err != nil {
		return metadata, fmt.Errorf("competitive map %d %s: %w", mapID, fileName, err)
	}
	metadata.spawnGroupA, itemTableOffset, err = parseCompetitiveCellTable(mapBytes, itemTableOffset, width, height, competitiveMapMaxSpawnCells, "spawn group A")
	if err != nil {
		return metadata, fmt.Errorf("competitive map %d %s: %w", mapID, fileName, err)
	}
	metadata.spawnGroupB, itemTableOffset, err = parseCompetitiveCellTable(mapBytes, itemTableOffset, width, height, competitiveMapMaxSpawnCells, "spawn group B")
	if err != nil {
		return metadata, fmt.Errorf("competitive map %d %s: %w", mapID, fileName, err)
	}
	metadata.objectiveCells, itemTableOffset, err = parseCompetitiveCellTable(mapBytes, itemTableOffset, width, height, uint64(width)*uint64(height), "objective")
	if err != nil {
		return metadata, fmt.Errorf("competitive map %d %s: %w", mapID, fileName, err)
	}
	if itemTableOffset != len(mapBytes) {
		return metadata, fmt.Errorf("competitive map %d %s has %d unparsed trailing bytes", mapID, fileName, len(mapBytes)-itemTableOffset)
	}
	metadata.battlefield, err = parseCompetitiveBattlefield(elements, mapBytes)
	if err != nil {
		return metadata, fmt.Errorf("competitive map %d %s: %w", mapID, fileName, err)
	}
	metadata.airborneCells, err = parseCompetitiveAirborneCells(metadata.battlefield)
	if err != nil {
		return metadata, fmt.Errorf("competitive map %d %s: %w", mapID, fileName, err)
	}
	return metadata, nil
}

const (
	competitiveMapWidthV3          = 15
	competitiveMapHeightV3         = 13
	competitiveMapTileLayers       = 3
	competitiveMapMaxWallItemRules = 100
	competitiveMapMaxSpawnCells    = 8
)

func competitiveWallItemTableOffset(mapBytes []byte) (int, error) {
	headerSize, width, height, err := competitiveMapGeometry(mapBytes)
	if err != nil {
		return 0, err
	}
	offset := uint64(headerSize) + uint64(competitiveMapTileLayers)*uint64(width)*uint64(height)*4
	if offset+4 > uint64(len(mapBytes)) {
		return 0, fmt.Errorf("wall-item table offset %d exceeds file length %d", offset, len(mapBytes))
	}
	return int(offset), nil
}

func competitiveMapGeometry(mapBytes []byte) (headerSize int, width, height uint32, err error) {
	if len(mapBytes) < 12 {
		return 0, 0, 0, fmt.Errorf("header is truncated")
	}
	version := binary.LittleEndian.Uint32(mapBytes[0:4])
	headerSize = 12
	width, height = uint32(competitiveMapWidthV3), uint32(competitiveMapHeightV3)
	switch version {
	case 3:
	case 4:
		if len(mapBytes) < 20 {
			return 0, 0, 0, fmt.Errorf("version 4 header is truncated")
		}
		headerSize = 20
		width = binary.LittleEndian.Uint32(mapBytes[12:16])
		height = binary.LittleEndian.Uint32(mapBytes[16:20])
	default:
		return 0, 0, 0, fmt.Errorf("unsupported map version %d", version)
	}
	if width == 0 || height == 0 || width > 256 || height > 256 {
		return 0, 0, 0, fmt.Errorf("invalid dimensions %dx%d", width, height)
	}
	return headerSize, width, height, nil
}

func parseCompetitiveAirborneCells(field CompetitiveBattlefield) ([]CompetitiveCell, error) {
	width, height := uint32(field.Width), uint32(field.Height)
	// NOTIFY_DISPATCH_BOMB stores row/column in one byte but the final client
	// validates the native 13x15 battlefield bounds before creating an object.
	// FUN_005b8574 queries the runtime CMapElem pointer grid, so candidates must
	// come from the same positive-anchor/configured-footprint projection as the
	// battle grid. Serialized negative editor markers do not occupy a cell by
	// themselves.
	if width > competitiveMapWidthV3 || height > competitiveMapHeightV3 {
		return nil, fmt.Errorf("dimensions %dx%d exceed dispatch-bomb bounds %dx%d", width, height, competitiveMapWidthV3, competitiveMapHeightV3)
	}
	if int(width)*int(height) != len(field.Cells) {
		return nil, fmt.Errorf("battlefield has %d cells, want %d for %dx%d", len(field.Cells), width*height, width, height)
	}
	cells := make([]CompetitiveCell, 0, width*height)
	for row := uint32(0); row < height; row++ {
		for col := uint32(0); col < width; col++ {
			cell := field.Cells[int(row*width+col)]
			if cell.MapElementOccupied {
				continue
			}
			cells = append(cells, CompetitiveCell{Row: byte(row), Col: byte(col)})
		}
	}
	return cells, nil
}

func parseCompetitiveWallItemRules(mapBytes []byte, offset int) ([]CompetitiveWallItemRule, int, error) {
	count := binary.LittleEndian.Uint32(mapBytes[offset : offset+4])
	if count > competitiveMapMaxWallItemRules {
		return nil, offset, fmt.Errorf("wall-item rule count %d exceeds %d", count, competitiveMapMaxWallItemRules)
	}
	offset += 4
	if uint64(offset)+uint64(count)*16 > uint64(len(mapBytes)) {
		return nil, offset, fmt.Errorf("%d wall-item rules are truncated", count)
	}
	rules := make([]CompetitiveWallItemRule, 0, count)
	seen := make(map[uint32]struct{}, count)
	for index := uint32(0); index < count; index++ {
		sceneID := binary.LittleEndian.Uint32(mapBytes[offset : offset+4])
		minimum := binary.LittleEndian.Uint32(mapBytes[offset+4 : offset+8])
		maximum := binary.LittleEndian.Uint32(mapBytes[offset+8 : offset+12])
		probability := math.Float32frombits(binary.LittleEndian.Uint32(mapBytes[offset+12 : offset+16]))
		offset += 16
		if sceneID == 0 || minimum > maximum || maximum > math.MaxInt16 || math.IsNaN(float64(probability)) || math.IsInf(float64(probability), 0) || probability < 0 || probability > 1 {
			return nil, offset, fmt.Errorf("wall-item rule %d is invalid: id=%d min=%d max=%d probability=%g", index, sceneID, minimum, maximum, probability)
		}
		if _, duplicate := seen[sceneID]; duplicate {
			return nil, offset, fmt.Errorf("wall-item rule %d repeats scene ID %d", index, sceneID)
		}
		seen[sceneID] = struct{}{}
		rules = append(rules, CompetitiveWallItemRule{SceneID: sceneID, Minimum: int16(minimum), Maximum: int16(maximum), Probability: probability})
	}
	return rules, offset, nil
}

func parseCompetitiveCellTable(mapBytes []byte, offset int, width, height uint32, maximum uint64, name string) ([]CompetitiveCell, int, error) {
	if offset < 0 || uint64(offset)+4 > uint64(len(mapBytes)) {
		return nil, offset, fmt.Errorf("%s cell count is truncated", name)
	}
	count := binary.LittleEndian.Uint32(mapBytes[offset : offset+4])
	offset += 4
	if uint64(count) > maximum {
		return nil, offset, fmt.Errorf("%s cell count %d exceeds %d", name, count, maximum)
	}
	if uint64(offset)+uint64(count)*4 > uint64(len(mapBytes)) {
		return nil, offset, fmt.Errorf("%d %s cells are truncated", count, name)
	}
	cells := make([]CompetitiveCell, 0, count)
	seen := make(map[CompetitiveCell]struct{}, count)
	for index := uint32(0); index < count; index++ {
		row := uint32(binary.LittleEndian.Uint16(mapBytes[offset : offset+2]))
		col := uint32(binary.LittleEndian.Uint16(mapBytes[offset+2 : offset+4]))
		offset += 4
		if row >= height || col >= width {
			return nil, offset, fmt.Errorf("%s cell %d coordinate %d,%d exceeds %dx%d", name, index, row, col, height, width)
		}
		cell := CompetitiveCell{Row: byte(row), Col: byte(col)}
		if _, duplicate := seen[cell]; duplicate {
			return nil, offset, fmt.Errorf("%s cell %d repeats coordinate %d,%d", name, index, row, col)
		}
		seen[cell] = struct{}{}
		cells = append(cells, cell)
	}
	return cells, offset, nil
}

// RollWallItems applies the local server's deterministic policy to the
// server-owned half of the original map format: each embedded rule is gated
// by its probability, then chooses a quantity in its inclusive range. The
// original client proves that GAME_BEGIN supplies these quantities and that
// ItemSeed is used only for the native coordinate shuffle; it does not contain
// Tencent's retired server-side random-number algorithm. Keeping this policy
// deterministic makes a captured GAME_BEGIN replayable without presenting the
// xorshift sequence below as recovered official-server behavior.
//
// The local policy additionally reserves a map-gated floor for the
// three native base attributes (SceneID 1/2/3) whenever the selected map
// actually defines them. The fallback for one rule is
// 3+ceil(players/2), no more than its map maximum. A normal probability roll
// above this floor is preserved when conservation-first transfers let every
// eligible type reach its floor. If any type remains deficient, guarantee mode
// jointly compresses all three target quantities into at most half of the
// ordinary hidden-item cells. Outside guarantee mode, successful native rolls
// may exceed that half-wall boundary. The final combined roll is always bounded
// by all ordinary hidden-item cells. Rule-owned objective carriers are a
// separate native pool and are not counted in this capacity.
// capacity is the number of ordinary hidden-item cells. baseCapacity is
// normally half of that capacity; both limits are enforced independently.
func (entry CompetitiveMap) RollWallItems(itemSeed uint32, participantCount, capacity, baseCapacity int) []CompetitiveWallItem {
	if participantCount < 1 {
		participantCount = 1
	}
	if capacity <= 0 {
		return nil
	}
	if baseCapacity < 0 {
		baseCapacity = 0
	}
	if baseCapacity > capacity {
		baseCapacity = capacity
	}
	state := itemSeed ^ entry.ID*0x9E3779B9
	if state == 0 {
		state = 0xA341316C
	}
	next := func() uint32 {
		state ^= state << 13
		state ^= state >> 17
		state ^= state << 5
		return state
	}
	quantities := make([]int16, len(entry.WallItemRules))
	floors := make([]int16, len(entry.WallItemRules))
	rollQuantity := func(rule CompetitiveWallItemRule, probability float64) int16 {
		if probability <= 0 {
			return 0
		}
		minimum := rule.Minimum
		if minimum == 0 && rule.Maximum > 0 {
			minimum = 1
		}
		if minimum > rule.Maximum || rule.Maximum == 0 {
			return 0
		}
		if probability < 1 && float64(next())/4294967296.0 >= probability {
			return 0
		}
		quantity := minimum
		if width := uint32(rule.Maximum-minimum) + 1; width > 1 {
			quantity += int16(next() % width)
		}
		// Keep the native-table RNG draws so disabling an inert item does not
		// reroll unrelated categories. It receives no cell in the allocation.
		if entry.SuppressCompetitiveWallItem(rule.SceneID) {
			return 0
		}
		return quantity
	}
	// Phase one owns the three base attributes and therefore their wall cells.
	for index, rule := range entry.WallItemRules {
		if rule.SceneID < 1 || rule.SceneID > 3 || rule.Probability <= 0 || rule.Maximum <= 0 || rule.Minimum > rule.Maximum {
			continue
		}
		probability := float64(rule.Probability)
		if probability > 1 {
			probability = 1
		}
		quantities[index] = rollQuantity(rule, probability)
		floor := int16(3 + (participantCount+1)/2)
		if floor > rule.Maximum {
			floor = rule.Maximum
		}
		floors[index] = floor
	}
	allocated := finalizeBaseWallItemQuantities(entry.WallItemRules, quantities, floors, capacity, baseCapacity)
	baseQuantity := 0
	for index, quantity := range allocated {
		if entry.WallItemRules[index].SceneID >= 1 && entry.WallItemRules[index].SceneID <= 3 {
			baseQuantity += int(quantity)
		}
	}
	remainingCapacity := capacity - baseQuantity
	// Phase two rolls every other map-defined reward into the remaining wall
	// cells. A damped opportunity compensation keeps its chance from collapsing
	// merely because base progression was assigned first, without linearly
	// doubling probabilities or turning every high-probability rule into a
	// guaranteed drop when half of the ordinary cells remain.
	if remainingCapacity > 0 {
		nonBaseQuantities := make([]int16, len(quantities))
		for index, rule := range entry.WallItemRules {
			if rule.SceneID >= 1 && rule.SceneID <= 3 {
				continue
			}
			probability := compensatedWallItemProbability(float64(rule.Probability), capacity, remainingCapacity)
			nonBaseQuantities[index] = rollQuantity(rule, probability)
		}
		nonBaseAllocated := allocateWallItemQuantities(nonBaseQuantities, remainingCapacity, itemSeed^entry.ID^0xA17E4E5D)
		for index, quantity := range nonBaseAllocated {
			if entry.WallItemRules[index].SceneID < 1 || entry.WallItemRules[index].SceneID > 3 {
				allocated[index] = quantity
			}
		}
	}
	items := make([]CompetitiveWallItem, 0, len(entry.WallItemRules))
	for _, baseOnly := range []bool{true, false} {
		for index, quantity := range allocated {
			isBase := entry.WallItemRules[index].SceneID >= 1 && entry.WallItemRules[index].SceneID <= 3
			if quantity > 0 && isBase == baseOnly {
				items = append(items, CompetitiveWallItem{SceneID: entry.WallItemRules[index].SceneID, Quantity: quantity})
			}
		}
	}
	return items
}

// finalizeBaseWallItemQuantities enters the half-wall guarantee mode only when
// conservation-first rebalancing still leaves an eligible type below its
// floor. In that mode all base quantities are jointly compressed into the
// guarantee budget. If every type reaches its floor through the native roll
// and transfers alone, the half-wall boundary is not applied and only the
// physical ordinary-wall capacity remains authoritative.
func finalizeBaseWallItemQuantities(rules []CompetitiveWallItemRule, quantities, floors []int16, capacity, baseCapacity int) []int16 {
	rebalanced := rebalanceBaseWallItemQuantities(rules, quantities, floors)
	needsGuarantee := false
	for index := range rebalanced {
		if floors[index] > 0 && rebalanced[index] < floors[index] {
			needsGuarantee = true
			break
		}
	}
	if !needsGuarantee {
		return allocateBaseWallItemQuantities(rules, rebalanced, capacity)
	}
	targets := append([]int16(nil), rebalanced...)
	for index := range targets {
		if targets[index] < floors[index] {
			targets[index] = floors[index]
		}
	}
	return allocateBaseWallItemQuantities(rules, targets, baseCapacity)
}

// rebalanceBaseWallItemQuantities conserves the native roll total while using
// an overrepresented base type to help a type that is still below its floor.
// A transfer never crosses the two quantities: after moving one cell the
// donor must remain at least as large as the receiver. Equal receivers prefer
// SceneID 1/2/3, while equal donors yield SceneID 3/2/1 first, preserving the
// declared priority only as a tie-break.
func rebalanceBaseWallItemQuantities(rules []CompetitiveWallItemRule, quantities, floors []int16) []int16 {
	result := append([]int16(nil), quantities...)
	for {
		receiver := -1
		for index, rule := range rules {
			if rule.SceneID < 1 || rule.SceneID > 3 || floors[index] <= 0 || result[index] >= floors[index] {
				continue
			}
			if receiver < 0 || result[index] < result[receiver] ||
				(result[index] == result[receiver] && rule.SceneID < rules[receiver].SceneID) {
				receiver = index
			}
		}
		if receiver < 0 {
			break
		}
		donor := -1
		for index, rule := range rules {
			if index == receiver || rule.SceneID < 1 || rule.SceneID > 3 || floors[index] <= 0 || result[index]-result[receiver] < 2 {
				continue
			}
			if donor < 0 || result[index] > result[donor] ||
				(result[index] == result[donor] && rule.SceneID > rules[donor].SceneID) {
				donor = index
			}
		}
		if donor < 0 {
			break
		}
		result[donor]--
		result[receiver]++
	}
	return result
}

func compensatedWallItemProbability(probability float64, capacity, remainingCapacity int) float64 {
	if probability <= 0 || capacity <= 0 || remainingCapacity <= 0 {
		return 0
	}
	if probability >= 1 || remainingCapacity >= capacity {
		return min(probability, 1)
	}
	// Treat the reduced position pool as fewer independent opportunities, but
	// damp the raw capacity ratio with its square root. Complement-space
	// scaling keeps every original probability below one strictly below one.
	opportunityScale := math.Sqrt(float64(capacity) / float64(remainingCapacity))
	return 1 - math.Pow(1-probability, opportunityScale)
}

func allocateBaseWallItemQuantities(rules []CompetitiveWallItemRule, quantities []int16, capacity int) []int16 {
	result := make([]int16, len(quantities))
	remaining := capacity
	// Capacity pressure is resolved by level-filling: the currently least
	// represented eligible base types receive the next cells first. SceneID
	// 1/2/3 order is only the tie-break when their allocated quantities are
	// equal, so a large bubble-capacity roll cannot starve power or speed of
	// their possible guarantees.
	for remaining > 0 {
		allocated := false
		for sceneID := uint32(1); sceneID <= 3 && remaining > 0; sceneID++ {
			for index, rule := range rules {
				if rule.SceneID != sceneID || result[index] >= quantities[index] {
					continue
				}
				result[index]++
				remaining--
				allocated = true
			}
		}
		if !allocated {
			break
		}
	}
	return result
}

func allocateWallItemQuantities(quantities []int16, capacity int, allocationSeed uint32) []int16 {
	result := make([]int16, len(quantities))
	remaining := capacity
	candidates := make([]int, 0, len(quantities))
	for index, quantity := range quantities {
		if quantity > 0 {
			candidates = append(candidates, index)
		}
	}
	if len(candidates) == 0 {
		return result
	}
	start := int(allocationSeed % uint32(len(candidates)))
	for remaining > 0 {
		allocated := false
		for offset := 0; offset < len(candidates); offset++ {
			index := candidates[(start+offset)%len(candidates)]
			if result[index] >= quantities[index] {
				continue
			}
			result[index]++
			remaining--
			allocated = true
			if remaining == 0 {
				break
			}
		}
		if !allocated {
			break
		}
	}
	return result
}

func loadMapHash(mapDirectory string, mapID uint32, fileName string) ([32]byte, error) {
	var result [32]byte
	if filepath.Base(fileName) != fileName {
		return result, fmt.Errorf("map %d has unsafe filename %q", mapID, fileName)
	}
	mapBytes, err := os.ReadFile(filepath.Join(mapDirectory, fileName))
	if err != nil {
		return result, fmt.Errorf("read map %d %s: %w", mapID, fileName, err)
	}
	copy(result[:], fmt.Sprintf("%X", md5.Sum(mapBytes)))
	return result, nil
}

func (catalog *Catalog) loadSequences(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open Continue.ini: %w", err)
	}
	defer file.Close()
	var current int32
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "[continue") && strings.HasSuffix(line, "]") {
			value := strings.TrimSuffix(strings.TrimPrefix(line, "[continue"), "]")
			parsed, parseErr := strconv.ParseInt(value, 10, 32)
			if parseErr != nil || parsed <= 0 {
				return fmt.Errorf("invalid Continue.ini section %q", line)
			}
			current = int32(parsed)
			continue
		}
		if current == 0 || !strings.HasPrefix(strings.ToLower(line), "maps") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			return fmt.Errorf("invalid map sequence line %q", line)
		}
		for _, token := range strings.Split(parts[1], ",") {
			parsed, parseErr := strconv.ParseUint(strings.TrimSpace(token), 10, 32)
			if parseErr != nil {
				return fmt.Errorf("invalid map ID in %q: %w", line, parseErr)
			}
			id := uint32(parsed)
			entry, exists := catalog.maps[id]
			if !exists {
				return fmt.Errorf("Continue.ini references missing PVE map %d", id)
			}
			entry.SequenceID = current
			entry.MapIndex = len(catalog.sequences[current])
			catalog.maps[id] = entry
			catalog.sequences[current] = append(catalog.sequences[current], id)
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read Continue.ini: %w", err)
	}
	if len(catalog.sequences) == 0 {
		return fmt.Errorf("Continue.ini contains no adventure sequences")
	}
	return nil
}

func (catalog *Catalog) Map(id uint32) (AdventureMap, bool) {
	entry, ok := catalog.maps[id]
	return entry, ok
}

func (catalog *Catalog) SelectableMap(id uint32) (AdventureMap, bool) {
	entry, ok := catalog.maps[id]
	return entry, ok && entry.SequenceID > 0 && entry.MapIndex == 0
}

func (catalog *Catalog) Next(currentID uint32) (AdventureMap, bool) {
	current, ok := catalog.maps[currentID]
	if !ok || current.SequenceID <= 0 || current.MapIndex < 0 {
		return AdventureMap{}, false
	}
	sequence := catalog.sequences[current.SequenceID]
	nextIndex := current.MapIndex + 1
	if nextIndex >= len(sequence) {
		return AdventureMap{}, false
	}
	return catalog.maps[sequence[nextIndex]], true
}

func (catalog *Catalog) SelectableIDs() []uint32 {
	var ids []uint32
	for id, entry := range catalog.maps {
		if entry.SequenceID > 0 && entry.MapIndex == 0 {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

// RandomSelectable chooses uniformly from installed route first stages. The
// client represents its default random-map choice by not sending a map-change
// request, so the authoritative choice must be made when the match starts.
func (catalog *Catalog) RandomSelectable() (AdventureMap, error) {
	ids := catalog.SelectableIDs()
	if len(ids) == 0 {
		return AdventureMap{}, fmt.Errorf("catalog contains no selectable adventure maps")
	}
	index, err := cryptorand.Int(cryptorand.Reader, big.NewInt(int64(len(ids))))
	if err != nil {
		return AdventureMap{}, fmt.Errorf("choose random adventure map: %w", err)
	}
	return catalog.maps[ids[index.Int64()]], nil
}

// CompetitiveMap returns any explicitly selected installed non-PVE map.
// The caller still validates the room population and field type.
func (catalog *Catalog) CompetitiveMap(id uint32) (CompetitiveMap, bool) {
	entry, ok := catalog.competitiveMaps[id]
	return cloneCompetitiveMap(entry), ok && entry.Selectable
}

// CompetitiveMapMetadata returns installed map metadata even when the
// original mapDesc entry is intentionally not selectable. Runtime room
// validation must continue using CompetitiveMap.
func (catalog *Catalog) CompetitiveMapMetadata(id uint32) (CompetitiveMap, bool) {
	entry, ok := catalog.competitiveMaps[id]
	return cloneCompetitiveMap(entry), ok
}

func cloneCompetitiveMap(entry CompetitiveMap) CompetitiveMap {
	entry.BossCandidates = append([]CompetitiveBossCandidate(nil), entry.BossCandidates...)
	for index := range entry.BossCandidates {
		entry.BossCandidates[index] = cloneCompetitiveBossCandidate(entry.BossCandidates[index])
	}
	entry.WallItemRules = append([]CompetitiveWallItemRule(nil), entry.WallItemRules...)
	entry.HiddenItemCells = append([]CompetitiveCell(nil), entry.HiddenItemCells...)
	entry.SpawnGroupA = append([]CompetitiveCell(nil), entry.SpawnGroupA...)
	entry.SpawnGroupB = append([]CompetitiveCell(nil), entry.SpawnGroupB...)
	entry.ObjectiveCells = append([]CompetitiveCell(nil), entry.ObjectiveCells...)
	entry.AirborneCells = append([]CompetitiveCell(nil), entry.AirborneCells...)
	entry.Battlefield = entry.Battlefield.Clone()
	return entry
}

func (catalog *Catalog) AllCompetitiveIDs() []uint32 {
	ids := make([]uint32, 0, len(catalog.competitiveMaps))
	for id := range catalog.competitiveMaps {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

func (catalog *Catalog) CompetitiveIDs() []uint32 {
	ids := make([]uint32, 0, len(catalog.competitiveMaps))
	for id, entry := range catalog.competitiveMaps {
		if entry.Selectable {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

// RandomOrdinaryCompetitive chooses a normal bubble-battle map compatible
// with the room's field and population. Special-rule families such as bun,
// bomb, and machine require their own settlement state machines and are never
// selected accidentally by the default random choice.
func (catalog *Catalog) RandomOrdinaryCompetitive(playerCount int, itemField byte) (CompetitiveMap, error) {
	return catalog.RandomEligibleOrdinaryCompetitive(playerCount, itemField, math.MaxUint32, true)
}

// RandomEligibleOrdinaryCompetitive chooses from the same native ordinary-map
// pool while enforcing mapDesc.py's competitive-points gate. bypassPoints is
// reserved for the original VIP card, whose client description explicitly
// permits creating any map without the required points.
func (catalog *Catalog) RandomEligibleOrdinaryCompetitive(playerCount int, itemField byte, points uint32, bypassPoints bool) (CompetitiveMap, error) {
	var ids []uint32
	for id, entry := range catalog.competitiveMaps {
		if !entry.Selectable || int(entry.PlayerLimit) < playerCount || entry.RequiredItemField != itemField {
			continue
		}
		if !bypassPoints && points < entry.RequiredPoints {
			continue
		}
		if !entry.UsesOrdinaryElimination() {
			continue
		}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return CompetitiveMap{}, fmt.Errorf("catalog contains no eligible ordinary competitive map for %d players, item field %d, and %d points", playerCount, itemField, points)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	index, err := cryptorand.Int(cryptorand.Reader, big.NewInt(int64(len(ids))))
	if err != nil {
		return CompetitiveMap{}, fmt.Errorf("choose random competitive map: %w", err)
	}
	return cloneCompetitiveMap(catalog.competitiveMaps[ids[index.Int64()]]), nil
}
