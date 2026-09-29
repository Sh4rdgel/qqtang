package mapdata

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
)

// CompetitiveCellCollision is the standard-rule collision property of one
// native map cell. Special-rule behaviours are intentionally not represented.
type CompetitiveCellCollision byte

const (
	CompetitiveCellOpen CompetitiveCellCollision = iota
	CompetitiveCellBreakable
	CompetitiveCellSolid
)

// CompetitiveBattlefield is an immutable row-major collision grid. The
// original client uses 40-pixel cells; keeping geometry in cells avoids
// leaking rendering coordinates into the deterministic battle core.
type CompetitiveBattlefield struct {
	Width  uint16
	Height uint16
	Cells  []CompetitiveBattleCell
}

// CompetitiveBattleCell keeps actor and flame traversal separate. The native
// GridAttr value exposes those as independent flags; collapsing them into one
// "solid" bit makes passable flame barriers and flame-passable scenery behave
// incorrectly in a simulator.
type CompetitiveBattleCell struct {
	Collision     CompetitiveCellCollision
	FlamePassable bool
	// NativeGridAttr preserves the full per-cell bitfield. Player directions
	// occupy bits 0..3; flame directions occupy bits 8..11 (005ef1aa/005ef20f).
	NativeGridAttr    uint32
	NativeGridAttrSet bool
	// FUN_005cfc10 queries only scene+0xe2c (serialized layer zero) at
	// the actor's origin. Destination collision still checks both layers.
	NativePlayerExitAttr    uint32
	NativePlayerExitAttrSet bool
	// MapElementOccupied is independent of actor traversal. Client.exe first
	// uses GridAttr bits 0..3 for directional actor traversal, but
	// its local bubble producer separately rejects every cell for which the
	// static map-element lookup returns an object.
	MapElementOccupied bool
	Durability         byte
	MapElementID       uint32
	NormalPushable     bool
	PandaPushable      bool
	ElementWidth       byte
	ElementHeight      byte
	ElementAnchorRow   int16
	ElementAnchorCol   int16
}

func (field CompetitiveBattlefield) Cell(row, col int) (CompetitiveBattleCell, bool) {
	if row < 0 || col < 0 || row >= int(field.Height) || col >= int(field.Width) {
		return CompetitiveBattleCell{Collision: CompetitiveCellSolid}, false
	}
	index := row*int(field.Width) + col
	if index < 0 || index >= len(field.Cells) {
		return CompetitiveBattleCell{Collision: CompetitiveCellSolid}, false
	}
	return field.Cells[index], true
}

func (field CompetitiveBattlefield) Clone() CompetitiveBattlefield {
	field.Cells = append([]CompetitiveBattleCell(nil), field.Cells...)
	return field
}

type competitiveMapElement struct {
	id        uint32
	lifeTime  int
	canMove   uint32
	width     int
	height    int
	gridAttrs []uint32
}

var (
	mapElementClassLine = regexp.MustCompile(`^class\s+QQTMapElem([0-9]+)\(QQTMapElem\):`)
	mapElementIntLine   = regexp.MustCompile(`^(LifeTime|imageID|canMove)\s*=\s*(-?[0-9]+)\s*$`)
	mapElementSizeLine  = regexp.MustCompile(`^size\s*=\s*\(\s*([0-9]+)\s*,\s*([0-9]+)\s*\)\s*$`)
	mapElementAttrsLine = regexp.MustCompile(`^GridAttr\s*=\s*\(([^)]*)\)\s*$`)
)

func loadCompetitiveMapElements(path string) (map[uint32]competitiveMapElement, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open map-element catalog: %w", err)
	}
	defer file.Close()

	elements := make(map[uint32]competitiveMapElement)
	var current *competitiveMapElement
	commit := func() error {
		if current == nil {
			return nil
		}
		if current.width <= 0 || current.height <= 0 {
			return fmt.Errorf("map element %d has invalid size %dx%d", current.id, current.width, current.height)
		}
		cellCount := current.width * current.height
		if len(current.gridAttrs) == 1 && cellCount > 1 {
			value := current.gridAttrs[0]
			current.gridAttrs = make([]uint32, cellCount)
			for index := range current.gridAttrs {
				current.gridAttrs[index] = value
			}
		}
		if len(current.gridAttrs) != cellCount {
			return fmt.Errorf("map element %d has %d grid attributes for %dx%d cells", current.id, len(current.gridAttrs), current.width, current.height)
		}
		if current.lifeTime > 255 {
			return fmt.Errorf("map element %d lifetime %d exceeds one-byte battle durability", current.id, current.lifeTime)
		}
		if current.width > 255 || current.height > 255 {
			return fmt.Errorf("map element %d footprint %dx%d exceeds one-byte battle metadata", current.id, current.width, current.height)
		}
		elements[current.id] = *current
		return nil
	}

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if fields := mapElementClassLine.FindStringSubmatch(line); fields != nil {
			if err := commit(); err != nil {
				return nil, err
			}
			id, _ := strconv.ParseUint(fields[1], 10, 32)
			current = &competitiveMapElement{id: uint32(id), lifeTime: -1, width: 1, height: 1, gridAttrs: []uint32{0}}
			continue
		}
		if current == nil {
			continue
		}
		if fields := mapElementIntLine.FindStringSubmatch(line); fields != nil {
			value, parseErr := strconv.ParseInt(fields[2], 10, 32)
			if parseErr != nil {
				return nil, fmt.Errorf("parse map element %d %s: %w", current.id, fields[1], parseErr)
			}
			switch fields[1] {
			case "LifeTime":
				current.lifeTime = int(value)
			case "imageID":
				current.id = uint32(value)
			case "canMove":
				if value < 0 {
					return nil, fmt.Errorf("map element %d has negative canMove %d", current.id, value)
				}
				current.canMove = uint32(value)
			}
			continue
		}
		if fields := mapElementSizeLine.FindStringSubmatch(line); fields != nil {
			width, _ := strconv.Atoi(fields[1])
			height, _ := strconv.Atoi(fields[2])
			current.width, current.height = width, height
			continue
		}
		if fields := mapElementAttrsLine.FindStringSubmatch(line); fields != nil {
			parts := strings.Split(fields[1], ",")
			attrs := make([]uint32, 0, len(parts))
			for _, part := range parts {
				part = strings.TrimSpace(part)
				if part == "" {
					continue
				}
				value, parseErr := strconv.ParseUint(part, 0, 32)
				if parseErr != nil {
					return nil, fmt.Errorf("parse map element %d GridAttr %q: %w", current.id, part, parseErr)
				}
				attrs = append(attrs, uint32(value))
			}
			if len(attrs) == 0 {
				return nil, fmt.Errorf("map element %d has empty GridAttr", current.id)
			}
			current.gridAttrs = attrs
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan map-element catalog: %w", err)
	}
	if err := commit(); err != nil {
		return nil, err
	}
	if len(elements) == 0 {
		return nil, fmt.Errorf("map-element catalog contains no elements")
	}
	return elements, nil
}

func parseCompetitiveBattlefield(elements map[uint32]competitiveMapElement, mapBytes []byte) (CompetitiveBattlefield, error) {
	headerSize, width, height, err := competitiveMapGeometry(mapBytes)
	if err != nil {
		return CompetitiveBattlefield{}, err
	}
	if width > 0xffff || height > 0xffff {
		return CompetitiveBattlefield{}, fmt.Errorf("battlefield dimensions %dx%d exceed uint16", width, height)
	}
	if len(elements) == 0 {
		return CompetitiveBattlefield{}, fmt.Errorf("map-element catalog is empty")
	}
	cellCount := int(width * height)
	field := CompetitiveBattlefield{Width: uint16(width), Height: uint16(height), Cells: make([]CompetitiveBattleCell, cellCount)}
	for index := range field.Cells {
		field.Cells[index] = CompetitiveBattleCell{Collision: CompetitiveCellOpen, FlamePassable: true,
			NativePlayerExitAttr: 15, NativePlayerExitAttrSet: true}
	}
	layerBytes := cellCount * 4
	if headerSize+2*layerBytes > len(mapBytes) {
		return CompetitiveBattlefield{}, fmt.Errorf("collision tile layers are truncated")
	}
	for layer := 0; layer < 2; layer++ {
		base := headerSize + layer*layerBytes
		for row := 0; row < int(height); row++ {
			for col := 0; col < int(width); col++ {
				raw := int32(binary.LittleEndian.Uint32(mapBytes[base+(row*int(width)+col)*4:]))
				// FUN_005d80e1 copies the serialized layer, but creates a
				// CMapElem only for positive entries. Negative entries are editor
				// continuation markers; they never create an object on their own.
				// FUN_005d839c then projects the positive anchor over the configured
				// mapElem.py footprint. Treating an orphan negative marker as an
				// occupied cell invents a wall that does not exist in Client.exe.
				if raw <= 0 {
					continue
				}
				element, ok := elements[uint32(raw)]
				if !ok {
					// A few special-rule maps reference server-era elements that
					// are absent from the shipped Python catalog. Preserve safety
					// by treating the positive anchor cell as solid. Its unknown
					// footprint cannot be inferred from negative editor markers. The
					// restricted rule-1 engine never assigns behaviour to it.
					previous := field.Cells[row*int(width)+col]
					if layer == 0 {
						previous.NativePlayerExitAttr = 0
					}
					field.Cells[row*int(width)+col] = CompetitiveBattleCell{Collision: CompetitiveCellSolid, MapElementOccupied: true,
						NativePlayerExitAttr: previous.NativePlayerExitAttr, NativePlayerExitAttrSet: true}
					continue
				}
				for localRow := 0; localRow < element.height; localRow++ {
					for localCol := 0; localCol < element.width; localCol++ {
						targetRow, targetCol := row+localRow, col+localCol
						if targetRow >= int(height) || targetCol >= int(width) {
							return CompetitiveBattlefield{}, fmt.Errorf("map element %d at %d,%d exceeds %dx%d", raw, row, col, width, height)
						}
						// The Python comment names four groups, not four individual
						// bits. Native movement/flame methods select a direction bit.
						attr := element.gridAttrs[localRow*element.width+localCol]
						// FUN_005d839c writes the object pointer and attributes into
						// every footprint cell. A later positive anchor replaces the
						// earlier pointer at overlapping cells, so native row-major
						// installation is last-writer-wins rather than a union of
						// collision flags.
						projected := CompetitiveBattleCell{MapElementOccupied: true, FlamePassable: attr&0xf00 != 0, NativeGridAttr: attr, NativeGridAttrSet: true}
						projected.NativePlayerExitAttr = field.Cells[targetRow*int(width)+targetCol].NativePlayerExitAttr
						projected.NativePlayerExitAttrSet = true
						if layer == 0 {
							projected.NativePlayerExitAttr = attr
						}
						// LifeTime is independent of player traversal. Grass (for
						// example element 6003) is both walkable and destructible.
						if element.lifeTime > 0 {
							projected.Durability = byte(element.lifeTime)
						}
						if attr&0xf == 0 {
							projected.Collision = CompetitiveCellSolid
							if element.lifeTime > 0 {
								projected.Collision = CompetitiveCellBreakable
							}
						}
						// Client.exe exposes two independent map-element predicates to
						// the push producer. A normal actor takes the map-element vtable
						// +0x38 path (canMove bit 1, numeric value 2), while Panda avatar
						// 44 takes +0x34 (LifeTime>=0). Those producer predicates are
						// insufficient: FUN_005d8900 rejects any footprint wider or
						// taller than one cell, including after a 0xFB3 confirmation.
						// Pig bed 9008 has canMove=2 but is 2x1 and stays occupied.
						movableSize := element.width == 1 && element.height == 1
						normalPushable := movableSize && attr&0xf == 0 && element.canMove&2 != 0
						pandaPushable := movableSize && attr&0xf == 0 && element.lifeTime >= 0
						if normalPushable || pandaPushable || element.lifeTime > 0 {
							projected.MapElementID = element.id
							projected.NormalPushable = normalPushable
							projected.PandaPushable = pandaPushable
							projected.ElementWidth = byte(element.width)
							projected.ElementHeight = byte(element.height)
							projected.ElementAnchorRow = int16(row)
							projected.ElementAnchorCol = int16(col)
						}
						field.Cells[targetRow*int(width)+targetCol] = projected
					}
				}
			}
		}
	}
	return field, nil
}
