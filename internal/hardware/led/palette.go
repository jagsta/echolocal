package led

import "math"

type Palette []Color

func (p Palette) At(x float64) Color {
	switch len(p) {
	case 0:
		return Color{}
	case 1:
		return p[0]
	}

	x = math.Mod(math.Mod(x, 1)+1, 1) * float64(len(p))
	i := int(x)
	return blend(p[i], p[(i+1)%len(p)], x-float64(i))
}

func (p Palette) Along(x float64) Color {
	if len(p) < 2 {
		return p.At(x)
	}

	x = math.Max(0, math.Min(1, x)) * float64(len(p)-1)
	i := int(x)
	if i >= len(p)-1 {
		return p[len(p)-1]
	}
	return blend(p[i], p[i+1], x-float64(i))
}

func (p Palette) Shade(x, dim float64) Color {
	if len(p) > 1 {
		dim = 0.5 + 0.5*dim
	}
	return scale(p.Along(x), dim)
}

func (p Palette) Nth(i int) Color {
	if len(p) == 0 {
		return Color{}
	}
	return p[((i%len(p))+len(p))%len(p)]
}

var wheel = func() Palette {
	p := make(Palette, 6)
	for i := range p {
		p[i] = scale(hue(float64(i)/float64(len(p))), 0.6)
	}
	return p
}()

// flame is roughly a candle at 1900 K.
var flame = Palette{{R: 0xFF, G: 0x93, B: 0x29}}

var fire = Palette{
	{R: 0xFF, G: 0xE0, B: 0xA0},
	{R: 0xFF, G: 0x90, B: 0x20},
	{R: 0xD0, G: 0x30, B: 0x00},
	{R: 0x40, G: 0x04, B: 0x00},
}

var crimson = Palette{
	{R: 0x28, G: 0x00, B: 0x04},
	{R: 0x80, G: 0x00, B: 0x08},
	{R: 0xD0, G: 0x00, B: 0x14},
	{R: 0xFF, G: 0x40, B: 0x48},
}

var aurora = Palette{
	{R: 0x00, G: 0xC8, B: 0x50},
	{R: 0x00, G: 0xB0, B: 0xB0},
	{R: 0x30, G: 0x40, B: 0xC0},
	{R: 0x70, G: 0x20, B: 0xA0},
	{R: 0x10, G: 0x90, B: 0x80},
}

var ocean = Palette{
	{R: 0x00, G: 0x0C, B: 0x40},
	{R: 0x00, G: 0x50, B: 0xA0},
	{R: 0x00, G: 0xB0, B: 0xC0},
	{R: 0x90, G: 0xF0, B: 0xE0},
}

var ice = Palette{
	{R: 0x08, G: 0x18, B: 0x50},
	{R: 0x10, G: 0x60, B: 0xC0},
	{R: 0x60, G: 0xC0, B: 0xF0},
	{R: 0xE0, G: 0xF8, B: 0xFF},
}

var sunset = Palette{
	{R: 0x30, G: 0x08, B: 0x60},
	{R: 0xA0, G: 0x10, B: 0x60},
	{R: 0xE0, G: 0x50, B: 0x20},
	{R: 0xFF, G: 0xB0, B: 0x30},
}

var forest = Palette{
	{R: 0x04, G: 0x28, B: 0x10},
	{R: 0x10, G: 0x70, B: 0x20},
	{R: 0x40, G: 0xB0, B: 0x20},
	{R: 0xA0, G: 0xE0, B: 0x40},
}

var alarm = Palette{
	{R: 0x30, G: 0x00, B: 0x00},
	{R: 0xC0, G: 0x00, B: 0x00},
	{R: 0xFF, G: 0x30, B: 0x18},
	{R: 0xFF, G: 0xB0, B: 0xA0},
}

// alexaGreen is the stock Alexa drop-in green (the green sweep the Echo line
// shows for an incoming drop-in and while one is connected). Palette-driven
// effects (Drop-in, green Pulse) lock their colour off the claim's base, so
// the recolour holds whatever the user's ring light is set to.
var alexaGreen = Palette{
	{R: 0x00, G: 0x28, B: 0x08},
	{R: 0x00, G: 0x80, B: 0x20},
	{R: 0x00, G: 0xE0, B: 0x48},
	{R: 0x60, G: 0xFF, B: 0x90},
}

// alexaPurple is the stock Alexa announce purple (D-ANN-11): the violet
// breathe Amazon's line shows while an announcement plays. Palette-driven
// like alexaGreen, so the recolour holds whatever the claim's base is.
var alexaPurple = Palette{
	{R: 0x18, G: 0x00, B: 0x30},
	{R: 0x50, G: 0x00, B: 0x90},
	{R: 0x90, G: 0x30, B: 0xE0},
	{R: 0xC0, G: 0x90, B: 0xFF},
}

var vu = Palette{
	{R: 0x00, G: 0xB0, B: 0x18},
	{R: 0x50, G: 0xC0, B: 0x00},
	{R: 0xE0, G: 0x90, B: 0x00},
	{R: 0xFF, G: 0x10, B: 0x00},
}

var duo = Palette{
	{R: 0x00, G: 0xE0, B: 0xC0},
	{R: 0xE0, G: 0x00, B: 0xA0},
}

var pellets = Palette{
	{R: 0xFF, G: 0xD0, B: 0x00},
	{R: 0x30, G: 0x30, B: 0x38},
}
