package main

import (
	rl "github.com/gen2brain/raylib-go/raylib"
)

func main() {
	rl.SetConfigFlags(rl.FlagMsaa4xHint)
	rl.InitWindow(1280, 800, "SPHEREx Sky Timeline")
	rl.SetTargetFPS(60)

	app := NewApp()
	platformInit(app)

	for !rl.WindowShouldClose() {
		dt := float64(rl.GetFrameTime())
		platformUpdate(app, dt)
		app.Update(dt)
		app.viewer.Input(app)

		rl.BeginDrawing()
		app.viewer.Draw(app)
		rl.EndDrawing()
	}

	rl.CloseWindow()
}
