//go:build payload

package parts

import _ "embed"

//go:embed payload/echolocal-airplay
var airplay []byte

func AirPlay() []byte { return airplay }
