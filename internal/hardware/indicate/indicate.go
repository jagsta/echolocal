package indicate

import "time"

type Priority int

const (
	PriorityBase Priority = iota
	PriorityRoom
	PriorityMute
	PriorityTimer
	PriorityBusy
	PriorityTurn
	PriorityAlarm
	PriorityNotice
	PriorityTrouble
	PriorityBoot
)

type Color struct{ R, G, B byte }

type Room struct {
	Level func() float64

	Facing func() (float64, bool)

	Beam func() (float64, bool)
}

type Claim interface {
	Play(effect string, base Color)
	PlayReversed(effect string, base Color)
	React(effect string, base Color, room Room)
	ShowFor(effect string, base Color, d time.Duration)
	Clear()
	Release()
}

type Surface interface {
	Claim(Priority) Claim

	HoldOnStop(Color)

	Present() bool
}
