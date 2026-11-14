//go:build !(js && wasm)

package main

import (
	"encoding/json"
	"os"

	rl "github.com/gen2brain/raylib-go/raylib"

	"spherex/internal/sky"
)

// The desktop build has no HTML around it, so a few keys stand in for the UI:
// space plays, arrows step, 1-6 pick a channel, E/D/S switch mode, F flags,
// C catalog, T tint, N next test object. It needs the server running;
// SPHEREX_SIM=1 shows the simulation instead of archive images.
var native struct {
	targets []struct {
		Name string  `json:"name"`
		RA   float64 `json:"ra"`
		Dec  float64 `json:"dec"`
		Band int     `json:"band"`
		Mode string  `json:"mode"`
	}
	target  int
	playing bool
	timer   float64
}

func pointerInside() bool { return rl.IsCursorOnScreen() }

const wheelSign = 1

func platformInit(a *App) {
	a.api = os.Getenv("SPHEREX_API")
	if a.api == "" {
		a.api = "http://localhost:8080"
	}
	body, err := a.get("/api/targets")
	if err == nil {
		err = json.Unmarshal(body, &native.targets)
	}
	if err != nil || len(native.targets) == 0 {
		println("no test objects from", a.api, "- is the server running?", err)
		return
	}
	native.playing = true
	openTarget(a, 0)
}

func openTarget(a *App, i int) {
	t := native.targets[i]
	a.Open(OpenRequest{RA: t.RA, Dec: t.Dec, FOV: 6, Name: t.Name, Real: os.Getenv("SPHEREX_SIM") != "1"})
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
		native.target = (native.target + 1) % len(native.targets)
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
