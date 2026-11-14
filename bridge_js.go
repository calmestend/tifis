//go:build js && wasm

package main

import (
	"encoding/json"
	"syscall/js"

	rl "github.com/gen2brain/raylib-go/raylib"
)

// platformInit exposes the engine to the page as globalThis.skyEngine and sends
// events to globalThis.skyUI.event(kind, json). The HTML side owns the app
// state (URL, playback); the engine renders and measures.
func platformInit(a *App) {
	// The page and the IRSA proxy share the origin.
	a.api = js.Global().Get("location").Get("origin").String()
	ui := js.Global().Get("skyUI")
	a.emit = func(kind string, payload any) {
		b, err := json.Marshal(payload)
		if err != nil {
			println("skyEngine:", kind, err.Error())
			return
		}
		if ui.Truthy() {
			ui.Call("event", kind, string(b))
		}
	}

	fn := func(f func(args []js.Value)) js.Func {
		return js.FuncOf(func(_ js.Value, args []js.Value) any {
			f(args)
			return nil
		})
	}
	api := map[string]any{
		"open": fn(func(args []js.Value) {
			var r OpenRequest
			if json.Unmarshal([]byte(args[0].String()), &r) == nil {
				a.Post(func() { a.Open(r) })
			}
		}),
		"view": fn(func(args []js.Value) {
			var v ViewState
			if json.Unmarshal([]byte(args[0].String()), &v) == nil {
				a.Post(func() { a.SetView(v) })
			}
		}),
		"zoom": fn(func(args []js.Value) {
			factor := float32(args[0].Float())
			a.Post(func() {
				a.viewer.ZoomBy(factor, float32(rl.GetScreenWidth())/2, float32(rl.GetScreenHeight())/2)
			})
		}),
		"fit": fn(func([]js.Value) {
			a.Post(a.viewer.Fit)
		}),
		"resize": fn(func(args []js.Value) {
			w, h := args[0].Int(), args[1].Int()
			a.Post(func() { rl.SetWindowSize(w, h) })
		}),
		"clearSelection": fn(func([]js.Value) {
			a.Post(a.ClearSelection)
		}),
		"pointer": fn(func(args []js.Value) {
			inside := args[0].Bool()
			a.Post(func() { pointerIn = inside })
		}),
	}
	js.Global().Set("skyEngine", js.ValueOf(api))
	a.emit("ready", map[string]any{"targets": targets()})
}

func platformUpdate(*App, float64) {}

// pointerIn is reported by the page: RGFW on the web does not track whether
// the cursor is over the canvas.
var pointerIn bool

func pointerInside() bool { return pointerIn }

// The web backend reports the browser's deltaY, positive when scrolling down,
// which is the opposite of the desktop convention.
const wheelSign = -1
