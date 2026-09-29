package battleengine

import "testing"

func TestTacticalRefugeCanFinishEnteringCellBeforeTurning(t *testing.T) {
	c := testConfig()
	c.Rules.TickMS = 20
	c.Rules.BombFuseMS = 1200
	c.Participants[0].Spawn = Cell{Row: 0, Col: 1}
	c.Participants[0].SpeedPixelsPerSecond = 100
	c.Participants[0].BombPower = 1
	c.Participants[1].Spawn = Cell{Row: 2, Col: 4}
	for i := range c.Grid.Cells {
		c.Grid.Cells[i] = Tile{Kind: CellSolid}
	}
	for _, cell := range []Cell{{Row: 0, Col: 1}, {Row: 1, Col: 1}, {Row: 1, Col: 0}, {Row: 2, Col: 4}} {
		c.Grid.Cells[int(cell.Row)*int(c.Grid.Width)+int(cell.Col)] = Tile{Kind: CellOpen, FlamePassable: true}
	}
	e := mustEngine(t, c)
	for i := 0; i < 13; i++ {
		if _, err := e.Step([]Action{{PlayerID: 1, Move: DirectionDown, PlaceBomb: i == 0}}); err != nil {
			t.Fatal(err)
		}
	}
	if e.actors[0].Position.Cell() != (Cell{Row: 1, Col: 1}) {
		t.Fatalf("fixture did not enter corner: %+v", e.actors[0].Position)
	}
	sealed := e.Clone()
	sealed.grid.Cells[int(c.Grid.Width)] = Tile{Kind: CellSolid}
	if tacticalFactsForTest(t, sealed, 1).CurrentDirectionWholeCellRefugeFound[DirectionDown] {
		t.Fatal("centering invented a route through the sealed exit")
	}
	tooLate := e.Clone()
	tooLate.bombs[0].ExplodeAtMS = e.elapsedMS + e.rules.TickMS
	if tacticalFactsForTest(t, tooLate, 1).CurrentDirectionWholeCellRefugeFound[DirectionDown] {
		t.Fatal("centering did not charge time before the imminent blast")
	}
	facts := tacticalFactsForTest(t, e, 1)
	if !facts.CurrentDirectionWholeCellRefugeFound[DirectionDown] {
		t.Errorf("continuing down to centre and turning left has a native refuge: %+v", facts.CurrentDirectionWholeCellRefugeFound)
	}
	// Native execution is the positive evidence, not another copy of the search.
	for e.elapsedMS < 1600 {
		dir := DirectionNone
		if e.actors[0].Position.Y < 60 {
			dir = DirectionDown
		} else if e.actors[0].Position.X > 20 {
			dir = DirectionLeft
		}
		if _, err := e.Step([]Action{{PlayerID: 1, Move: dir}}); err != nil {
			t.Fatal(err)
		}
	}
	if e.actors[0].State != ActorActive || e.actors[0].Position.Cell() != (Cell{Row: 1, Col: 0}) {
		t.Fatalf("native positive escape failed: %+v", e.actors[0])
	}
}
