package sysimg

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

// A modified system boots only with both the fstab verify flag and the verity metadata gone.
const VerityOffset = 798945280

const (
	StockMagic    = "01b001b0"
	DisabledMagic = "564f4646" // "VOFF"
)

var DisabledBytes = []byte("VOFF")

// A Fire OS OTA writes the other slot and switches to it.
const (
	OTAKey   = "ro.build.version.number"
	OTAValue = "4503599628273540"
)

type Patch struct {
	File   string
	Path   string
	SHA256 string
	Size   int64
}

var (
	DefaultProp = Patch{
		File:   "biscuit/default.prop",
		Path:   "default.prop",
		SHA256: "9090f348d70531d9f9794ae3036481f4508ea20dbedd703465338f1ce4169a1d",
		Size:   579,
	}
	Fstab = Patch{
		File:   "biscuit/fstab.mt8163",
		Path:   "fstab.mt8163",
		SHA256: "43d9039682c9af8c9f3b59eceb6e1baaf03abb5d575123df071f6d3f5961d881",
		Size:   1470,
	}
	AthenaFOSFlags = Patch{
		File:   "athena/init.fosflags.sh",
		Path:   "init.fosflags.sh",
		SHA256: "074f8fe778b3d651d8f90d2f5d338f98ef44d1662e31b0d08f2d525c62f4ecad",
		Size:   5114,
	}
	AthenaCameraServer = Patch{
		File:   "athena/cameraserver.rc",
		Path:   "system/etc/init/cameraserver.rc",
		SHA256: "8b057791062d3b52ac90c9a6c59f5adc5d4844b8e92302aebe81e621913c4b08",
		Size:   351,
	}
	AthenaConnectivity = Patch{
		File:   "athena/init.connectivity.rc",
		Path:   "vendor/etc/init/hw/init.connectivity.rc",
		SHA256: "2961e3dc4a511e5d0fb468c174beaba79cc76077c603c3e4e76560f942c7665f",
		Size:   1320,
	}
)

// movs r0, #1; bx lr
var Adbd = BinPatch{
	Path:   "sbin/adbd",
	Offset: 105460,
	Before: []byte{0x10, 0xb5, 0x02, 0x20},
	After:  []byte{0x01, 0x20, 0x70, 0x47},
}

func (p Patch) Verify(data []byte) error {
	if int64(len(data)) != p.Size {
		return fmt.Errorf("sysimg: %s is %d bytes, want %d", p.Path, len(data), p.Size)
	}
	sum := sha256.Sum256(data)
	if got := hex.EncodeToString(sum[:]); got != p.SHA256 {
		return fmt.Errorf("sysimg: %s hashes to %s, want %s", p.Path, got, p.SHA256)
	}
	return nil
}

func Verity(magic []byte, at int64) (disabled bool, err error) {
	switch hex.EncodeToString(magic) {
	case DisabledMagic:
		return true, nil
	case StockMagic:
		return false, nil
	}
	return false, fmt.Errorf(
		"sysimg: no verity metadata at %d, found %x",
		at,
		bytes.TrimRight(magic, "\x00"),
	)
}
