//go:build voice

package parts

import _ "embed"

//go:embed payload/echolocal-voice
var voice []byte

func Voice() []byte { return voice }
