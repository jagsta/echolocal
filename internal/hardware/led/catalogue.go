package led

// Alexa's wake/listen indication is a cyan/blue sweep; the amber chase was the
// pre-Alexa convention. Chase is palette-driven: white falls back to a plain
// chase, ListenBlue renders blue dots on the dark ring.
var alexaBlue = Palette{
	{R: 0x00, G: 0x30, B: 0xB0},
	{R: 0x00, G: 0x70, B: 0xE0},
	{R: 0x40, G: 0xB0, B: 0xFF},
	{R: 0xC0, G: 0xE8, B: 0xFF},
}

var ambientEffects = []Effect{
	{Name: EffectPulse, New: breathe},
	{Name: EffectBlueChase, Palette: alexaBlue, New: chase},
	{Name: EffectGreenPulse, Palette: alexaGreen, New: breathe},
	// D-ANN-11: announce playback breathe, palette-locked to Alexa's purple
	// (same shape as the green lane — see alexaPurple in palette.go).
	{Name: EffectAnnounce, Palette: alexaPurple, New: breathe},
	{Name: EffectHeartbeat, New: heartbeat},
	{Name: EffectRipple, New: ripple},
	{Name: EffectStandingWave, New: standing},
	{Name: EffectTwinkle, New: twinkle},

	{Name: EffectCrimsonHeartbeat, Palette: crimson, New: heartbeat},
	{Name: EffectAuroraPulse, Palette: aurora, New: breathe},
	{Name: EffectCandle, Palette: flame, New: flicker},
	{Name: EffectFireplace, Palette: fire, New: flicker},
	{Name: EffectEmbers, Palette: fire, New: embers},
	{Name: EffectAurora, Palette: aurora, New: drift},
	{Name: EffectSunsetDrift, Palette: sunset, New: drift},
	{Name: EffectOceanRipple, Palette: ocean, New: ripple},
	{Name: EffectRainbowTwinkle, Palette: wheel, New: twinkle},
	{Name: EffectForestTwinkle, Palette: forest, New: twinkle},
}

var motionEffects = []Effect{
	{Name: EffectComet, New: comet},
	{Name: EffectChase, New: chase},
	{Name: EffectScanner, New: scanner},
	{Name: EffectPinwheel, New: pinwheel},
	{Name: EffectSpiral, New: spiral},
	{Name: EffectWipe, New: wipe},
	{Name: EffectHelix, New: helix},
	{Name: EffectOrbits, New: orbits},
	{Name: EffectBounce, New: bounce},
	{Name: EffectSpring, New: spring},

	{Name: EffectRainbow, Palette: wheel, New: spin},
	{Name: EffectFireComet, Palette: fire, New: comet},
	{Name: EffectIceComet, Palette: ice, New: comet},
	{Name: EffectRainbowChase, Palette: wheel, New: chase},
	{Name: EffectIceScanner, Palette: ice, New: scanner},
	{Name: EffectRainbowPinwheel, Palette: wheel, New: pinwheel},
	{Name: EffectSunsetSpiral, Palette: sunset, New: spiral},
	{Name: EffectRainbowOrbits, Palette: wheel, New: orbits},
	{Name: EffectDNA, Palette: duo, New: helix},
	{Name: EffectPacMan, Palette: pellets, New: pacman},
}

var alertEffects = []Effect{
	{Name: EffectAlert, Palette: alarm, New: alert},
	{Name: EffectBeacon, Palette: alarm, New: beacon},
	// Beacon is palette-locked to alarm (red), so the stock Alexa drop-in
	// green sweep needs its own entry: the same beacon sweep in Alexa's green.
	{Name: EffectDropIn, Palette: alexaGreen, New: beacon},
}

var roomEffects = []Effect{
	{Name: EffectFollower, Senses: follower},
	{Name: EffectBeamCompass, Senses: beamFollower},
	{Name: EffectRoomGlow, Senses: roomGlow},
	{Name: EffectRoomMeter, Senses: roomMeter},
	{Name: EffectRoomOcean, Palette: ocean, Senses: roomMeter},
	{Name: EffectRoomVU, Palette: vu, Senses: roomMeter},
	{Name: EffectRoomFire, Palette: fire, Senses: roomFire},
	{Name: EffectRoomSpin, Palette: wheel, Senses: roomSpin},

	{Name: EffectRoomAurora, Palette: aurora, Senses: byRoom(drift)},
	{Name: EffectRoomTwinkle, Palette: wheel, Senses: byRoom(twinkle)},
	{Name: EffectRoomEmbers, Palette: fire, Senses: byRoom(embers)},
}
