package main

import (
	"fmt"
	"image/color"
	"math"

	rl "github.com/gen2brain/raylib-go/raylib"

	"spherex/internal/sky"
)

// Kanagawa Dragon, matching ui/style.css.
var (
	colBackground = color.RGBA{13, 12, 12, 255}    // dragonBlack0
	colFrame      = color.RGBA{57, 56, 54, 255}    // dragonBlack5
	colEmpty      = color.RGBA{24, 22, 22, 255}    // dragonBlack3
	colLabel      = color.RGBA{197, 201, 197, 255} // dragonWhite
	colFlag       = color.RGBA{162, 146, 163, 140} // dragonPink
	colFlagLine   = color.RGBA{220, 205, 222, 210}
	colCatalog    = color.RGBA{139, 164, 176, 235} // dragonBlue2
	colSelect     = color.RGBA{196, 178, 138, 255} // dragonYellow
	colGrid       = fade(colLabel, 22)
	colHover      = fade(colLabel, 160)
)

func fade(c color.RGBA, alpha uint8) color.RGBA {
	c.A = alpha
	return c
}

// Overlays sit on grayscale sky, where the muted theme colors get lost, so
// lines, rings and labels get a dark outline.
var colOutline = fade(colBackground, 200)

func drawRing(c rl.Vector2, r float32, col color.RGBA) {
	rl.DrawRing(c, r-2.5, r+1.5, 0, 360, 48, colOutline)
	rl.DrawRing(c, r-1.5, r+0.5, 0, 360, 48, col)
}

func drawLabel(text string, x, y int32, col color.RGBA) {
	for _, d := range [][2]int32{{-1, 0}, {1, 0}, {0, -1}, {0, 1}, {1, 1}} {
		rl.DrawText(text, x+d[0], y+d[1], 20, colOutline)
	}
	rl.DrawText(text, x, y, 20, col)
}

type Viewer struct {
	tex      rl.Texture2D
	texN     int
	hasImage bool

	n            int
	zoom         float32 // screen pixels per image pixel
	cx, cy       float32 // image point at the center of the screen
	fitted       bool
	dragging     bool
	moved        bool
	press        rl.Vector2
	pressCX      float32
	pressCY      float32
	hoverX       int
	hoverY       int
	lastW, lastH int
	userMoved    bool // zoomed or panned by hand, so resizing keeps the view
}

func NewViewer() *Viewer {
	return &Viewer{hoverX: -1, hoverY: -1}
}

func (v *Viewer) Reset(n int) {
	v.n = n
	v.fitted = false
	v.hasImage = false
}

func (v *Viewer) fitZoom() float32 {
	w, h := float32(rl.GetScreenWidth()), float32(rl.GetScreenHeight())
	return min(w, h) * 0.92 / float32(max(v.n, 1))
}

func (v *Viewer) Fit() {
	v.zoom = v.fitZoom()
	v.cx, v.cy = float32(v.n)/2, float32(v.n)/2
	v.fitted = true
	v.userMoved = false
}

func (v *Viewer) ZoomBy(factor float32, sx, sy float32) {
	ix, iy := v.toImage(sx, sy)
	fit := v.fitZoom()
	v.zoom = max(fit*0.5, min(v.zoom*factor, 80))
	// Keep the point under the cursor still.
	w, h := float32(rl.GetScreenWidth()), float32(rl.GetScreenHeight())
	v.cx = ix - (sx-w/2)/v.zoom
	v.cy = iy - (sy-h/2)/v.zoom
	v.userMoved = true
}

func (v *Viewer) SetImage(px []color.RGBA, n int) {
	if px == nil {
		v.hasImage = false
		return
	}
	if v.texN != n {
		if v.texN != 0 {
			rl.UnloadTexture(v.tex)
		}
		img := rl.GenImageColor(n, n, rl.Black)
		v.tex = rl.LoadTextureFromImage(img)
		rl.UnloadImage(img)
		rl.SetTextureFilter(v.tex, rl.FilterPoint)
		v.texN = n
	}
	rl.UpdateTexture(v.tex, px)
	v.hasImage = true
}

func (v *Viewer) toScreen(ix, iy float64) rl.Vector2 {
	w, h := float32(rl.GetScreenWidth()), float32(rl.GetScreenHeight())
	return rl.NewVector2((float32(ix)-v.cx)*v.zoom+w/2, (float32(iy)-v.cy)*v.zoom+h/2)
}

func (v *Viewer) toImage(sx, sy float32) (float32, float32) {
	w, h := float32(rl.GetScreenWidth()), float32(rl.GetScreenHeight())
	return (sx-w/2)/v.zoom + v.cx, (sy-h/2)/v.zoom + v.cy
}

func (v *Viewer) Input(a *App) {
	if v.n == 0 {
		return
	}
	w, h := rl.GetScreenWidth(), rl.GetScreenHeight()
	if !v.fitted || w != v.lastW || h != v.lastH {
		if !v.fitted || !v.userMoved {
			v.Fit()
		}
		v.lastW, v.lastH = w, h
	}

	m := rl.GetMousePosition()
	inside := pointerInside() && m.X >= 0 && m.Y >= 0 && m.X < float32(w) && m.Y < float32(h)

	if wheel := rl.GetMouseWheelMove() * wheelSign; wheel != 0 && inside {
		step := float32(1.15)
		if wheel < 0 {
			step = 1 / step
		}
		v.ZoomBy(step, m.X, m.Y)
	}

	if rl.IsMouseButtonPressed(rl.MouseButtonLeft) && inside {
		v.dragging, v.moved = true, false
		v.press, v.pressCX, v.pressCY = m, v.cx, v.cy
	}
	if v.dragging {
		d := rl.Vector2Subtract(m, v.press)
		if rl.Vector2Length(d) > 4 {
			v.moved = true
		}
		if v.moved {
			v.userMoved = true
			v.cx = v.pressCX - d.X/v.zoom
			v.cy = v.pressCY - d.Y/v.zoom
		}
		if rl.IsMouseButtonReleased(rl.MouseButtonLeft) || !rl.IsMouseButtonDown(rl.MouseButtonLeft) {
			v.dragging = false
			if !v.moved {
				ix, iy := v.toImage(m.X, m.Y)
				a.Select(float64(ix), float64(iy))
			}
		}
	}

	hx, hy := -1, -1
	if inside {
		ix, iy := v.toImage(m.X, m.Y)
		if ix >= 0 && iy >= 0 && int(ix) < v.n && int(iy) < v.n {
			hx, hy = int(ix), int(iy)
		}
	}
	if hx != v.hoverX || hy != v.hoverY {
		v.hoverX, v.hoverY = hx, hy
		a.hover(hx, hy)
	}
}

func (v *Viewer) Draw(a *App) {
	rl.ClearBackground(colBackground)
	if v.n == 0 || a.field == nil {
		return
	}
	f := a.field
	n := float32(v.n)
	o := v.toScreen(0, 0)
	size := n * v.zoom

	rl.DrawRectangleLinesEx(rl.NewRectangle(o.X-2, o.Y-2, size+4, size+4), 1, colFrame)
	if v.hasImage {
		rl.DrawTexturePro(v.tex, rl.NewRectangle(0, 0, n, n), rl.NewRectangle(o.X, o.Y, size, size), rl.Vector2{}, 0, rl.White)
	} else {
		rl.DrawRectangleV(o, rl.NewVector2(size, size), colEmpty)
	}

	if v.zoom >= 14 {
		v.drawGrid()
	}
	if a.view.Flags && a.shownFlags != nil && v.hasImage {
		v.drawFlags(a.shownFlags)
	}
	v.drawReticle(f)
	mjd := f.Visits[a.view.Visit].MJD
	if a.view.Catalog {
		v.drawCatalog(f, mjd)
	}
	if a.sel != nil && a.sel.Motion != nil {
		v.drawSelection(a, mjd)
	}
	if v.hoverX >= 0 {
		p := v.toScreen(float64(v.hoverX), float64(v.hoverY))
		rl.DrawRectangleLinesEx(rl.NewRectangle(p.X, p.Y, v.zoom, v.zoom), 1, colHover)
	}
	v.drawScaleBar()
	v.drawCompass()
}

func (v *Viewer) drawGrid() {
	w, h := float32(rl.GetScreenWidth()), float32(rl.GetScreenHeight())
	x0, y0 := v.toImage(0, 0)
	x1, y1 := v.toImage(w, h)
	i0, i1 := max(0, int(x0)), min(v.n, int(x1)+1)
	j0, j1 := max(0, int(y0)), min(v.n, int(y1)+1)
	top, bottom := v.toScreen(0, 0).Y, v.toScreen(0, float64(v.n)).Y
	left, right := v.toScreen(0, 0).X, v.toScreen(float64(v.n), 0).X
	for i := i0; i <= i1; i++ {
		x := v.toScreen(float64(i), 0).X
		rl.DrawLineV(rl.NewVector2(x, top), rl.NewVector2(x, bottom), colGrid)
	}
	for j := j0; j <= j1; j++ {
		y := v.toScreen(0, float64(j)).Y
		rl.DrawLineV(rl.NewVector2(left, y), rl.NewVector2(right, y), colGrid)
	}
}

// drawFlags shades flagged pixels and hatches them, so the cue does not rely on
// color alone.
func (v *Viewer) drawFlags(flags []sky.Flag) {
	for i, fl := range flags {
		if !fl.Bad() {
			continue
		}
		p := v.toScreen(float64(i%v.n), float64(i/v.n))
		rl.DrawRectangleV(p, rl.NewVector2(v.zoom, v.zoom), colFlag)
		if v.zoom >= 6 {
			rl.DrawLineEx(p, rl.NewVector2(p.X+v.zoom, p.Y+v.zoom), 1, colFlagLine)
		}
	}
}

func (v *Viewer) drawReticle(f *sky.Field) {
	c := v.toScreen(f.WCS.CX, f.WCS.CY)
	gap := max(10, 4*v.zoom)
	l := float32(10)
	col := fade(colLabel, 90)
	rl.DrawLineEx(rl.NewVector2(c.X-gap-l, c.Y), rl.NewVector2(c.X-gap, c.Y), 1.5, col)
	rl.DrawLineEx(rl.NewVector2(c.X+gap, c.Y), rl.NewVector2(c.X+gap+l, c.Y), 1.5, col)
	rl.DrawLineEx(rl.NewVector2(c.X, c.Y-gap-l), rl.NewVector2(c.X, c.Y-gap), 1.5, col)
	rl.DrawLineEx(rl.NewVector2(c.X, c.Y+gap), rl.NewVector2(c.X, c.Y+gap+l), 1.5, col)
}

// drawCatalog marks catalog positions at the Gaia epoch with an arrow to where
// the proper motion predicts the source on the current frame.
func (v *Viewer) drawCatalog(f *sky.Field, mjd float64) {
	for i := range f.Sources {
		s := &f.Sources[i]
		// Faint catalog stars that SPHEREx cannot see would only clutter the
		// view; fast movers are always worth an arrow.
		fast := math.Hypot(s.PMRA, s.PMDec) > 100
		if !s.Catalog || (s.CatalogMag > 16 && !fast && i != f.Target) {
			continue
		}
		x0, y0 := f.WCS.ToPixel(s.Position(s.CatalogEpoch()))
		x1, y1 := f.WCS.ToPixel(s.Position(mjd))
		if x1 < 0 || y1 < 0 || x1 > float64(f.N) || y1 > float64(f.N) {
			continue
		}
		p0, p1 := v.toScreen(x0, y0), v.toScreen(x1, y1)
		r := max(5, 1.6*v.zoom)
		if i == f.Target {
			drawRing(p0, r, colCatalog)
			drawLabel(s.Name, int32(p0.X+r+4), int32(p0.Y-r-20), colCatalog)
		} else {
			drawRing(p0, r*0.7, fade(colCatalog, 170))
		}
		if math.Hypot(x1-x0, y1-y0) > 0.25 {
			drawArrow(p0, p1, colCatalog)
		}
	}
}

func drawArrow(from, to rl.Vector2, col color.RGBA) {
	rl.DrawLineEx(from, to, 4, colOutline)
	rl.DrawLineEx(from, to, 2, col)
	d := rl.Vector2Normalize(rl.Vector2Subtract(to, from))
	nrm := rl.NewVector2(-d.Y, d.X)
	back := rl.Vector2Subtract(to, rl.Vector2Scale(d, 9))
	rl.DrawTriangle(to, rl.Vector2Add(back, rl.Vector2Scale(nrm, 5)), rl.Vector2Subtract(back, rl.Vector2Scale(nrm, 5)), col)
	rl.DrawTriangle(to, rl.Vector2Subtract(back, rl.Vector2Scale(nrm, 5)), rl.Vector2Add(back, rl.Vector2Scale(nrm, 5)), col)
}

func (v *Viewer) drawSelection(a *App, mjd float64) {
	f, s := a.field, a.sel
	m := s.Motion
	x, y := m.At(mjd)
	c := v.toScreen(x, y)
	drawRing(c, float32(sky.ApertureRadius)*v.zoom, colSelect)
	faint := fade(colSelect, 150)
	drawRing(c, float32(sky.AnnulusInner)*v.zoom, faint)
	drawRing(c, float32(sky.AnnulusOuter)*v.zoom, faint)

	if m.Moving && len(f.Visits) > 1 {
		ax, ay := m.At(f.Visits[0].MJD)
		bx, by := m.At(f.Visits[len(f.Visits)-1].MJD)
		rl.DrawLineEx(v.toScreen(ax, ay), v.toScreen(bx, by), 1.5, faint)
	}
	if a.view.Band == s.Band {
		for _, p := range m.Points {
			col := fade(colSelect, 110)
			if p.Visit == a.view.Visit {
				col = colSelect
			}
			rl.DrawCircleV(v.toScreen(p.X, p.Y), max(1.5, v.zoom*0.08), col)
		}
	}
}

func (v *Viewer) drawScaleBar() {
	h := float32(rl.GetScreenHeight())
	pxPerArcsec := v.zoom / float32(sky.PixScale)
	length := float32(5)
	for _, l := range []float32{5, 10, 15, 30, 60, 120, 300, 600} {
		length = l
		if l*pxPerArcsec >= 70 {
			break
		}
	}
	w := length * pxPerArcsec
	x, y := float32(18), h-24
	rl.DrawRectangleV(rl.NewVector2(x, y), rl.NewVector2(w, 4), colLabel)
	rl.DrawRectangleV(rl.NewVector2(x, y-5), rl.NewVector2(2, 14), colLabel)
	rl.DrawRectangleV(rl.NewVector2(x+w-2, y-5), rl.NewVector2(2, 14), colLabel)
	label := fmt.Sprintf("%.0f\"", length)
	if length >= 60 {
		label = fmt.Sprintf("%.0f'", length/60)
	}
	drawLabel(label, int32(x+w+8), int32(y-8), colLabel)
}

func (v *Viewer) drawCompass() {
	w := float32(rl.GetScreenWidth())
	c := rl.NewVector2(w-36, 52)
	l := float32(26)
	drawArrow(c, rl.NewVector2(c.X, c.Y-l), colLabel)
	drawArrow(c, rl.NewVector2(c.X-l, c.Y), colLabel)
	drawLabel("N", int32(c.X-5), int32(c.Y-l-24), colLabel)
	drawLabel("E", int32(c.X-l-18), int32(c.Y-9), colLabel)
}
