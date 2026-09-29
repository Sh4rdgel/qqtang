package battleengine

import "fmt"

// NativeHumanMovement contains authenticated PLAYER_MOVE path hints. The live
// mirror consumes the path between checkpoints instead of leaving humans
// stationary. This is clock-aligned interpolation, not the client's visual
// rollback/blending queue (005d09d6/005d1211).
type NativeHumanMovement struct {
	Sequence             uint16
	TimeMS               uint32
	Current, Corner, End Position
	Direction            Direction
	Speed                byte
	Moving, Forced       bool
}

type nativeHumanMotion struct {
	last    NativeHumanMovement
	current NativeHumanMovement
	active  bool
	queue   []NativeHumanMovement
}

func (runtime *Runtime) ReconcileHumanMovementPath(playerID uint16, move NativeHumanMovement) error {
	if runtime == nil || runtime.engine == nil {
		return fmt.Errorf("battle runtime is nil")
	}
	engine := runtime.engine
	index := engine.actorIndex(playerID)
	if index < 0 || engine.actors[index].Source != ParticipantHuman {
		return fmt.Errorf("movement player %d is not human", playerID)
	}
	if _, _, ok := move.Direction.delta(); !ok || move.Moving && move.Direction == DirectionNone {
		return fmt.Errorf("invalid native movement direction")
	}
	if _, ok := NativeSpeedPixelsPerSecond(move.Speed); !ok {
		return fmt.Errorf("invalid native movement speed %d", move.Speed)
	}
	for _, pos := range []Position{move.Current, move.Corner, move.End} {
		if pos.X < 0 || pos.Y < 0 || pos.X >= int32(engine.grid.Width)*CellSizePixels || pos.Y >= int32(engine.grid.Height)*CellSizePixels {
			return fmt.Errorf("native movement path outside map: %+v", pos)
		}
	}
	actor := &engine.actors[index]
	if actor.State == ActorEliminated {
		return nil
	}
	motion := actor.nativeHumanMotion
	if motion != nil {
		// Same segment heartbeats remain legal. Only a same-clock forced
		// checkpoint may replace a different path without advancing the clock.
		if move.TimeMS < motion.last.TimeMS || int16(move.Sequence-motion.last.Sequence) < 0 || move.TimeMS == motion.last.TimeMS && (!move.Forced || move == motion.last) {
			return nil
		}
	} else {
		motion = &nativeHumanMotion{}
		actor.nativeHumanMotion = motion
	}
	motion.last = move
	motion.queue = append(motion.queue, move)
	engine.advanceHumanMovement(actor, engine.elapsedMS)
	return nil
}

func (engine *Engine) advanceHumanMovement(actor *Actor, timeMS uint32) bool {
	motion := actor.nativeHumanMotion
	if motion == nil {
		return false
	}
	for len(motion.queue) > 0 && motion.queue[0].TimeMS <= timeMS {
		motion.current, motion.active = motion.queue[0], true
		motion.queue = motion.queue[1:]
	}
	if !motion.active || actor.State != ActorActive {
		return true
	}
	move := motion.current
	actor.nativePreviousPosition = actor.Position
	actor.Position = nativeHumanPathPosition(move, timeMS)
	if move.Direction != DirectionNone {
		actor.Facing = move.Direction
	}
	return true
}

func nativeHumanPathPosition(move NativeHumanMovement, timeMS uint32) Position {
	if !move.Moving || timeMS <= move.TimeMS {
		return move.Current
	}
	speed, _ := NativeSpeedPixelsPerSecond(move.Speed)
	distance := int64(speed) * int64(timeMS-move.TimeMS) / 1000
	position := move.Current
	var points [3]Position
	count := 0
	dx, dy, _ := move.Direction.delta()
	if move.Corner != (Position{}) {
		// Native path: travel along the held axis to the correction corner,
		// cross to the endpoint's lane, then continue to the endpoint. A later
		// heartbeat already beyond the corner must not walk back to it.
		beforeCorner := (move.Corner.X-position.X)*dx+(move.Corner.Y-position.Y)*dy >= 0
		if beforeCorner {
			corner := position
			if dx != 0 {
				corner.X = move.Corner.X
			} else {
				corner.Y = move.Corner.Y
			}
			points[count] = corner
			count++
			if dx != 0 {
				corner.Y = move.End.Y
			} else {
				corner.X = move.End.X
			}
			points[count] = corner
			count++
		}
	}
	points[count] = move.End
	count++
	for _, point := range points[:count] {
		// Wire paths are axis-aligned. Correct cross-axis quantization before
		// consuming distance on the held axis if no correction corner remains.
		if position.X != point.X && position.Y != point.Y {
			if dx != 0 {
				position.Y = point.Y
			} else {
				position.X = point.X
			}
		}
		x, y := int64(point.X-position.X), int64(point.Y-position.Y)
		length := x
		if length < 0 {
			length = -length
		}
		absY := y
		if absY < 0 {
			absY = -absY
		}
		length += absY
		if length <= distance {
			position = point
			distance -= length
			continue
		}
		if x > 0 {
			position.X += int32(distance)
		} else if x < 0 {
			position.X -= int32(distance)
		}
		if y > 0 {
			position.Y += int32(distance)
		} else if y < 0 {
			position.Y -= int32(distance)
		}
		break
	}
	return position
}

func stopNativeHumanMovement(actor *Actor) {
	if motion := actor.nativeHumanMotion; motion != nil {
		motion.active = false
		motion.queue = nil
	}
}
