package main

import (
	"image/color"
	"math"

	"spherex/internal/sky"
)

// Okabe-Ito colors, ordered from short to long wavelength; the UI uses the same.
var channelColors = [sky.NumChannels]color.RGBA{
	{0, 114, 178, 255},
	{86, 180, 233, 255},
	{0, 158, 115, 255},
	{240, 228, 66, 255},
	{230, 159, 0, 255},
	{213, 94, 0, 255},
}

type FrameInfo struct {
	Band      int     `json:"band"`
	Mode      string  `json:"mode"`
	Visit     int     `json:"visit"`
	Ref       int     `json:"ref"`
	Loaded    bool    `json:"loaded"`
	Available bool    `json:"available"`
	MJD       float64 `json:"mjd"`
	Date      string  `json:"date"`
	Lambda    float64 `json:"lambda"`
	RefLambda float64 `json:"refLambda,omitempty"`
	File      string  `json:"file"`
	Bg        float64 `json:"bg"`
	Sigma     float64 `json:"sigma"`
	Flagged   int     `json:"flagged"`
	Stacked   int     `json:"stacked,omitempty"`
}

// reference picks epoch A for the difference. Automatic mode looks in another
// season for a visit at nearly the same wavelength, as far away in time as
// possible: same color, longest baseline.
func (a *App) reference() int {
	f, v := a.field, a.view
	frames := f.Frames[v.Band]
	if v.Ref >= 0 && v.Ref < len(frames) && v.Ref != v.Visit {
		return v.Ref
	}
	cur := f.Visits[v.Visit]
	best, bestScore := -1, math.Inf(1)
	for i, fr := range frames {
		if i == v.Visit || !fr.Loaded || !fr.Available {
			continue
		}
		vis := f.Visits[i]
		dsub := math.Abs(vis.SubBand - cur.SubBand)
		score := dsub * 1000
		if dsub <= 1 {
			score = 0
		}
		if vis.Season == cur.Season {
			score += 1e6
		}
		score -= math.Abs(vis.MJD - cur.MJD)
		if score < bestScore {
			best, bestScore = i, score
		}
	}
	return best
}

func (a *App) compose() {
	f, v := a.field, a.view
	n := f.N
	fr := f.Frames[v.Band][v.Visit]
	st := a.stretch[v.Band]
	info := FrameInfo{
		Band: v.Band, Mode: v.Mode, Visit: v.Visit, Ref: -1,
		Loaded: fr.Loaded, Available: fr.Available,
		MJD: fr.MJD, Date: sky.MJDToTime(fr.MJD).Format("2006-01-02"),
		Lambda: fr.Lambda, File: fr.File, Bg: fr.Background, Sigma: fr.Sigma,
	}

	var vals []float32
	var flags []sky.Flag
	switch v.Mode {
	case "diff":
		ref := a.reference()
		info.Ref = ref
		if ref >= 0 {
			info.RefLambda = f.Frames[v.Band][ref].Lambda
		}
		if ref >= 0 && fr.Loaded && fr.Available {
			ra := f.Frames[v.Band][ref]
			vals = make([]float32, n*n)
			flags = make([]sky.Flag, n*n)
			for i := range vals {
				vals[i] = fr.Image[i] - ra.Image[i]
				flags[i] = fr.Flags[i] | ra.Flags[i]
			}
			sky.SubtractPlane(vals, flags, n, st.sigma*math.Sqrt2)
		}
	case "sum":
		lo := 0
		if v.Ref >= 0 && v.Ref <= v.Visit {
			lo = v.Ref
		}
		info.Ref = lo
		sum := make([]float64, n*n)
		cnt := make([]int, n*n)
		for i := lo; i <= v.Visit; i++ {
			s := f.Frames[v.Band][i]
			if !s.Loaded || !s.Available {
				continue
			}
			info.Stacked++
			off := st.median - s.Background
			for p, val := range s.Image {
				if !(s.Flags[p] &^ sky.FlagNonlinear).Bad() {
					sum[p] += float64(val) + off
					cnt[p]++
				}
			}
		}
		if info.Stacked > 0 {
			vals = make([]float32, n*n)
			flags = make([]sky.Flag, n*n)
			for p := range vals {
				if cnt[p] > 0 {
					vals[p] = float32(sum[p] / float64(cnt[p]))
				} else {
					vals[p] = float32(st.median)
					flags[p] = sky.FlagMissing
				}
			}
		}
		info.Loaded = info.Stacked > 0
	default:
		if fr.Loaded && fr.Available {
			vals, flags = fr.Image, fr.Flags
		}
	}

	for _, fl := range flags {
		if fl.Bad() {
			info.Flagged++
		}
	}

	switch {
	case vals != nil && st.ready:
		a.shown, a.shownFlags = vals, flags
		a.viewer.SetImage(a.colorize(vals), n)
	case fr.Loaded && !fr.Available:
		a.shown, a.shownFlags = nil, nil
		a.viewer.SetImage(nil, n)
	}
	a.shownInfo = info
	a.emit("frame", info)
}

func (a *App) colorize(vals []float32) []color.RGBA {
	v := a.view
	st := a.stretch[v.Band]
	out := make([]color.RGBA, len(vals))
	tint := channelColors[v.Band]
	if v.Mode == "diff" {
		norm := math.Asinh(20)
		for i, val := range vals {
			t := 0.5 + 0.5*math.Asinh(float64(val)/st.sigma/2)/norm
			g := uint8(255 * clamp01(t))
			out[i] = color.RGBA{g, g, g, 255}
		}
		return out
	}
	lo := math.Asinh(-0.75)
	hi := math.Asinh(st.high / st.sigma / 2)
	for i, val := range vals {
		t := clamp01((math.Asinh((float64(val)-st.median)/st.sigma/2) - lo) / (hi - lo))
		if !v.Tint {
			g := uint8(255 * t)
			out[i] = color.RGBA{g, g, g, 255}
			continue
		}
		w := t * t * t * t
		out[i] = color.RGBA{
			uint8(float64(tint.R)*t + (255-float64(tint.R))*w),
			uint8(float64(tint.G)*t + (255-float64(tint.G))*w),
			uint8(float64(tint.B)*t + (255-float64(tint.B))*w),
			255,
		}
	}
	return out
}

func clamp01(t float64) float64 {
	return math.Max(0, math.Min(1, t))
}

type hoverEvent struct {
	X     int      `json:"x"`
	Y     int      `json:"y"`
	RA    float64  `json:"ra"`
	Dec   float64  `json:"dec"`
	Value *float64 `json:"value"`
	Flags []string `json:"flags"`
}

func (a *App) hover(px, py int) {
	if a.field == nil || px < 0 || py < 0 || px >= a.field.N || py >= a.field.N {
		a.emit("hover", nil)
		return
	}
	ra, dec := a.field.WCS.ToSky(float64(px)+0.5, float64(py)+0.5)
	ev := hoverEvent{X: px, Y: py, RA: ra, Dec: dec}
	i := py*a.field.N + px
	if a.shown != nil {
		val := float64(a.shown[i])
		ev.Value = &val
		ev.Flags = a.shownFlags[i].Names()
	}
	a.emit("hover", ev)
}

// Select picks the source under (x, y); the server refines the position,
// follows it through the frames and measures it.
func (a *App) Select(x, y float64) {
	if a.field == nil {
		return
	}
	a.sel = &Selection{X: x, Y: y, Visit: a.view.Visit, Band: a.view.Band}
	a.selTimer = 0
	a.measureSelection()
}

func (a *App) ClearSelection() {
	a.sel = nil
	a.emit("select", nil)
}
