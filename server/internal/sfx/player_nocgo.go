//go:build !cgo

package sfx

import (
	"errors"

	"ridgericetalk/internal/config"
)

// errNoCGO is returned by playback when the binary was built without CGO (the opus
// codec and LiveKit publishing pipeline require CGO). Upload/binding APIs work fine.
var errNoCGO = errors.New("sfx playback requires CGO (opus codec); upload/binding still works")

// SfxPlayer is a no-op stub used in non-CGO builds. Method signatures must match
// player_cgo.go exactly so both build tags compile identically.
type SfxPlayer struct {
	cfg *config.Config
}

// NewSfxPlayer constructs the stub player.
func NewSfxPlayer(cfg *config.Config) *SfxPlayer {
	return &SfxPlayer{cfg: cfg}
}

// PlayOnce always fails in non-CGO builds.
func (p *SfxPlayer) PlayOnce(roomID, srcAbsPath string, volume int) error {
	return errNoCGO
}

// Disconnect is a no-op in non-CGO builds.
func (p *SfxPlayer) Disconnect() {}
