package mapdata

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

func TestWalkableGrassRetainsNativeLifetimeAndIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mapElem.py")
	// Exact shipped grass attributes; low bits permit actors and flames.
	if err := os.WriteFile(path, []byte("class QQTMapElem6003(QQTMapElem):\n    LifeTime = 1\n    imageID = 6003\n    GridAttr = (7967,)\n"), 0600); err != nil {
		t.Fatal(err)
	}
	elements, err := loadCompetitiveMapElements(path)
	if err != nil {
		t.Fatal(err)
	}
	contents := testCompetitiveMapBytes(1, nil)
	binary.LittleEndian.PutUint32(contents[12+(2*competitiveMapWidthV3+3)*4:], 6003)
	field, err := parseCompetitiveBattlefield(elements, contents)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := field.Cell(2, 3)
	if got.Collision != CompetitiveCellOpen || !got.FlamePassable || !got.MapElementOccupied || got.Durability != 1 || got.MapElementID != 6003 || got.ElementAnchorRow != 2 || got.ElementAnchorCol != 3 {
		t.Fatalf("grass lost orthogonal movement/damage properties: %+v", got)
	}
	if got.NormalPushable || got.PandaPushable {
		t.Fatalf("walkable grass gained a push rule: %+v", got)
	}
}

func TestShippedMapElementsPreserveEveryNativeFootprintAttribute(t *testing.T) {
	root := filepath.Join("..", "..", "..", "runtime", "client-patched")
	path := filepath.Join(root, "object", "mapElem", "mapElem.py")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Skip("client resources absent")
	}
	elements, err := loadCompetitiveMapElements(path)
	if err != nil {
		t.Fatal(err)
	}
	for id, element := range elements {
		contents := testCompetitiveMapBytes(1, nil)
		if element.width > competitiveMapWidthV3 || element.height > competitiveMapHeightV3 {
			t.Fatalf("uncovered element %d size %dx%d", id, element.width, element.height)
		}
		binary.LittleEndian.PutUint32(contents[12:], id)
		field, err := parseCompetitiveBattlefield(elements, contents)
		if err != nil {
			t.Fatalf("element %d: %v", id, err)
		}
		for row := 0; row < element.height; row++ {
			for col := 0; col < element.width; col++ {
				got, _ := field.Cell(row, col)
				attr := element.gridAttrs[row*element.width+col]
				if !got.NativeGridAttrSet || got.NativeGridAttr != attr || (got.Collision == CompetitiveCellOpen) != (attr&15 != 0) || got.FlamePassable != (attr&0xf00 != 0) || (got.Durability > 0) != (element.lifeTime > 0) {
					t.Fatalf("element %d part %d,%d attr %#x lost in projection: %+v", id, row, col, attr, got)
				}
			}
		}
	}
	// Water11 is also the Sailor Boss map: its underlying 5010 posts retain
	// the same 0x1515 directional mask, independent of the boss overlay.
	data, err := os.ReadFile(filepath.Join(root, "map", "water11_8.map"))
	if err != nil {
		t.Fatal(err)
	}
	field, err := parseCompetitiveBattlefield(elements, data)
	if err != nil {
		t.Fatal(err)
	}
	posts := 0
	for _, cell := range field.Cells {
		if cell.NativeGridAttrSet && cell.NativeGridAttr == 5397 {
			posts++
		}
	}
	if posts != 2 {
		t.Fatalf("water11 directional posts=%d want 2", posts)
	}
	t.Logf("audited %d element definitions and both Water11 posts", len(elements))
}
