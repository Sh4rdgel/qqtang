package mapdata

import (
	"encoding/binary"
	"testing"
)

func TestPlayerOriginExitKeepsFirstLayerSeparate(t *testing.T) {
	elements := map[uint32]competitiveMapElement{
		5018:  {id: 5018, width: 1, height: 1, lifeTime: -1, gridAttrs: []uint32{0}},
		17112: {id: 17112, width: 1, height: 1, lifeTime: -1, gridAttrs: []uint32{7196}},
	}
	for _, first := range []uint32{0, 17112} {
		data := testCompetitiveMapBytes(1, nil)
		index := 6*competitiveMapWidthV3 + 13
		binary.LittleEndian.PutUint32(data[12+index*4:], first)
		binary.LittleEndian.PutUint32(data[12+(competitiveMapWidthV3*competitiveMapHeightV3+index)*4:], 5018)
		field, err := parseCompetitiveBattlefield(elements, data)
		if err != nil {
			t.Fatal(err)
		}
		cell, _ := field.Cell(6, 13)
		want := uint32(15)
		if first != 0 {
			want = 7196
		}
		if cell.Collision != CompetitiveCellSolid || cell.NativeGridAttr != 0 || !cell.NativeGridAttrSet ||
			!cell.NativePlayerExitAttrSet || cell.NativePlayerExitAttr != want {
			t.Fatalf("layer1 origin %d lost under layer2 wall: %+v", first, cell)
		}
	}
}
