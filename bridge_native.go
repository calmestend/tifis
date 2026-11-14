//go:build !(js && wasm)

package main

import (
	"os"
	"time"

	rl "github.com/gen2brain/raylib-go/raylib"

	"spherex/internal/sky"
)

// The desktop build has no HTML around it, so a few keys stand in for the UI:
// space plays, arrows step, 1-6 pick a channel, E/D/S switch mode, F flags,
// C catalog, T tint, N next test object.
var native struct {
	target  int
	playing bool
	timer   float64
}

func pointerInside() bool { return rl.IsCursorOnScreen() }

const wheelSign = 1

func platformInit(a *App) {
	// SPHEREX_API=http://localhost:8080 loads real images through a running proxy.
	a.api = os.Getenv("SPHEREX_API")
	native.playing = true
	openTarget(a, 0)
}

func openTarget(a *App, i int) {
	t := sky.Targets[i]
	ra, dec := t.Source.Position(sky.TimeToMJD(time.Now()))
	a.Open(OpenRequest{RA: ra, Dec: dec, FOV: 6, Name: t.Name, Real: a.api != ""})
	a.SetView(ViewState{Band: t.Band, Mode: t.Mode, Ref: -1, Catalog: true, Flags: true})
}

func platformUpdate(a *App, dt float64) {
	if a.field == nil {
		return
	}
	v := a.view
	last := len(a.field.Visits) - 1
	switch {
	case rl.IsKeyPressed(rl.KeySpace):
		native.playing = !native.playing
	case rl.IsKeyPressed(rl.KeyRight):
		v.Visit = min(last, v.Visit+1)
	case rl.IsKeyPressed(rl.KeyLeft):
		v.Visit = max(0, v.Visit-1)
	case rl.IsKeyPressed(rl.KeyE):
		v.Mode = "epoch"
	case rl.IsKeyPressed(rl.KeyD):
		v.Mode = "diff"
	case rl.IsKeyPressed(rl.KeyS):
		v.Mode = "sum"
	case rl.IsKeyPressed(rl.KeyF):
		v.Flags = !v.Flags
	case rl.IsKeyPressed(rl.KeyC):
		v.Catalog = !v.Catalog
	case rl.IsKeyPressed(rl.KeyT):
		v.Tint = !v.Tint
	case rl.IsKeyPressed(rl.KeyN):
		native.target = (native.target + 1) % len(sky.Targets)
		openTarget(a, native.target)
		return
	}
	for k := range int32(sky.NumChannels) {
		if rl.IsKeyPressed(rl.KeyOne + k) {
			v.Band = int(k)
		}
	}
	if native.playing {
		native.timer += dt
		if native.timer > 0.3 {
			native.timer = 0
			v.Visit = (v.Visit + 1) % (last + 1)
		}
	}
	if v != a.view {
		a.SetView(v)
	}
}
