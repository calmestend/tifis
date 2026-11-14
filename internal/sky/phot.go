package sky

import (
	"math"
	"sort"
)

// Aperture and annulus radii follow the SPHEREx Source Discovery Tool.
const (
	ApertureRadius = 3.0 // pixels
	AnnulusInner   = 7.0
	AnnulusOuter   = 16.0
	detectionSNR   = 5
	// centroidBackground is a tighter annulus for centroiding, where speed
	// matters more than avoiding the PSF wings.
	centroidInner = 4.5
	centroidOuter = 7.5
)

// localBackground is the 3-sigma clipped median of the good annulus pixels
// around (x, y), as in the Source Discovery Tool.
func localBackground(fr *Frame, n int, x, y, rin, rout float64) float64 {
	var vals []float64
	r := int(math.Ceil(rout))
	for j := int(y) - r; j <= int(y)+r; j++ {
		for i := int(x) - r; i <= int(x)+r; i++ {
			if i < 0 || j < 0 || i >= n || j >= n || fr.Flags[j*n+i].Bad() {
				continue
			}
			d := math.Hypot(float64(i)+0.5-x, float64(j)+0.5-y)
			if d >= rin && d <= rout {
				vals = append(vals, float64(fr.Image[j*n+i]))
			}
		}
	}
	if len(vals) == 0 {
		return fr.Background
	}
	for range 5 {
		sort.Float64s(vals)
		med := vals[len(vals)/2]
		var ss float64
		for _, v := range vals {
			ss += (v - med) * (v - med)
		}
		std := math.Sqrt(ss / float64(len(vals)))
		kept := vals[:0]
		for _, v := range vals {
			if math.Abs(v-med) <= 3*std {
				kept = append(kept, v)
			}
		}
		if len(kept) == len(vals) || len(kept) < 5 {
			break
		}
		vals = kept
	}
	sort.Float64s(vals)
	return vals[len(vals)/2]
}

// Aperture sums background-subtracted pixel values inside ApertureRadius.
// Bad pixels count as background; ok is false when too many are bad. The
// error comes from the VARIANCE layer when there is one.
func Aperture(fr *Frame, n int, x, y float64) (flux, err float64, ok bool) {
	flux, err, good, npix := aperture(fr, n, x, y)
	return flux, err, npix > 0 && float64(npix-good) < 0.3*float64(npix)
}

func aperture(fr *Frame, n int, x, y float64) (flux, err float64, good, npix int) {
	bg := localBackground(fr, n, x, y, AnnulusInner, AnnulusOuter)
	r := int(math.Ceil(ApertureRadius))
	bad := 0
	variance := 0.0
	for j := int(y) - r; j <= int(y)+r; j++ {
		for i := int(x) - r; i <= int(x)+r; i++ {
			if math.Hypot(float64(i)+0.5-x, float64(j)+0.5-y) > ApertureRadius {
				continue
			}
			npix++
			if i < 0 || j < 0 || i >= n || j >= n || (fr.Flags[j*n+i] &^ FlagNonlinear).Bad() {
				bad++
				continue
			}
			flux += float64(fr.Image[j*n+i]) - bg
			if fr.Variance != nil {
				variance += float64(fr.Variance[j*n+i])
			}
		}
	}
	if fr.Variance != nil {
		err = math.Sqrt(variance)
	} else {
		nann := math.Pi * (AnnulusOuter*AnnulusOuter - AnnulusInner*AnnulusInner)
		err = fr.Sigma * math.Sqrt(float64(npix)+math.Pi/2*float64(npix*npix)/nann)
	}
	return flux, err, npix - bad, npix
}

// Centroid refines a position with a flux-weighted mean, moving at most maxShift
// pixels. snr reports how clearly there is a source there.
func Centroid(fr *Frame, n int, x, y, maxShift float64) (cx, cy, snr float64) {
	bg := localBackground(fr, n, x, y, centroidInner, centroidOuter)
	cx, cy = x, y
	for range 4 {
		var sw, sx, sy, sum float64
		npix := 0
		for j := int(cy) - 2; j <= int(cy)+2; j++ {
			for i := int(cx) - 2; i <= int(cx)+2; i++ {
				// Bright stars saturate and get their cores flagged, yet those
				// pixels still mark where the star is; only missing data is skipped.
				if i < 0 || j < 0 || i >= n || j >= n || fr.Flags[j*n+i]&FlagMissing != 0 {
					continue
				}
				px, py := float64(i)+0.5, float64(j)+0.5
				if math.Hypot(px-cx, py-cy) > 2 {
					continue
				}
				v := float64(fr.Image[j*n+i]) - bg
				sum += v
				npix++
				if v > 0 {
					sw += v
					sx += v * px
					sy += v * py
				}
			}
		}
		if npix == 0 || sw == 0 {
			return x, y, 0
		}
		snr = sum / (fr.Sigma * math.Sqrt(float64(npix)))
		nx, ny := sx/sw, sy/sw
		if d := math.Hypot(nx-x, ny-y); d > maxShift {
			nx, ny = x+(nx-x)*maxShift/d, y+(ny-y)*maxShift/d
		}
		cx, cy = nx, ny
	}
	return cx, cy, snr
}

type TrackPoint struct {
	Visit int     `json:"v"`
	X     float64 `json:"x"`
	Y     float64 `json:"y"`
}

// Motion is a linear fit of position against time, in pixels and days.
type Motion struct {
	X0, Y0, VX, VY, T0 float64
	ErrVX, ErrVY       float64 // standard errors of the velocity, px/day
	Points             []TrackPoint
	Moving             bool
}

func (m *Motion) At(mjd float64) (x, y float64) {
	return m.X0 + m.VX*(mjd-m.T0), m.Y0 + m.VY*(mjd-m.T0)
}

// ProperMotion in arcsec/yr, east and north positive.
func (m *Motion) ProperMotion() (pmRA, pmDec float64) {
	return -m.VX * PixScale * 365.25, -m.VY * PixScale * 365.25
}

// ProperMotionErr is the standard error of ProperMotion in arcsec/yr.
func (m *Motion) ProperMotionErr() (errRA, errDec float64) {
	return m.ErrVX * PixScale * 365.25, m.ErrVY * PixScale * 365.25
}

// Track follows the centroid of a source through every loaded frame of a
// channel, starting at the visit where it was picked, and fits its motion.
func (f *Field) Track(ch, visit int, x, y float64) *Motion {
	var order []int
	for v, fr := range f.Frames[ch] {
		if fr.Loaded && fr.Available {
			order = append(order, v)
		}
	}
	start := sort.SearchInts(order, visit)
	var pts []TrackPoint
	follow := func(idx []int) {
		gx, gy := x, y
		for _, v := range idx {
			cx, cy, snr := Centroid(f.Frames[ch][v], f.N, gx, gy, 1.5)
			if snr >= detectionSNR {
				gx, gy = cx, cy
				pts = append(pts, TrackPoint{v, cx, cy})
			}
		}
	}
	follow(order[start:])
	var back []int
	for i := start - 1; i >= 0; i-- {
		back = append(back, order[i])
	}
	follow(back)
	sort.Slice(pts, func(i, j int) bool { return pts[i].Visit < pts[j].Visit })

	m := &Motion{X0: x, Y0: y, Points: pts}
	if len(pts) == 0 {
		return m
	}
	m.fit(f, pts)
	// One pass of outlier rejection.
	var kept []TrackPoint
	for _, p := range pts {
		px, py := m.At(f.Visits[p.Visit].MJD)
		if math.Hypot(p.X-px, p.Y-py) < 0.6 {
			kept = append(kept, p)
		}
	}
	if len(kept) >= 2 && len(kept) < len(pts) {
		m.fit(f, kept)
	}
	return m
}

func (m *Motion) fit(f *Field, pts []TrackPoint) {
	var st, sx, sy float64
	tmin, tmax := math.Inf(1), math.Inf(-1)
	for _, p := range pts {
		t := f.Visits[p.Visit].MJD
		st += t
		sx += p.X
		sy += p.Y
		tmin, tmax = math.Min(tmin, t), math.Max(tmax, t)
	}
	k := float64(len(pts))
	m.T0, m.X0, m.Y0 = st/k, sx/k, sy/k
	m.VX, m.VY, m.Moving = 0, 0, false
	if tmax-tmin < 60 {
		return
	}
	var stt, stx, sty float64
	for _, p := range pts {
		dt := f.Visits[p.Visit].MJD - m.T0
		stt += dt * dt
		stx += dt * (p.X - m.X0)
		sty += dt * (p.Y - m.Y0)
	}
	m.VX, m.VY = stx/stt, sty/stt
	// Slope errors from the scatter around the fit.
	if n := len(pts); n > 2 {
		var rx, ry float64
		for _, p := range pts {
			x, y := m.At(f.Visits[p.Visit].MJD)
			rx += (p.X - x) * (p.X - x)
			ry += (p.Y - y) * (p.Y - y)
		}
		m.ErrVX = math.Sqrt(rx/float64(n-2)) / math.Sqrt(stt)
		m.ErrVY = math.Sqrt(ry/float64(n-2)) / math.Sqrt(stt)
	}
	// A motion counts when it is significant and above ~0.2 px/yr, below which
	// it is indistinguishable from noise at this pixel size.
	speed := math.Hypot(m.VX, m.VY)
	m.Moving = speed*365.25 > 0.2 && speed > 3*math.Hypot(m.ErrVX, m.ErrVY)
	if !m.Moving {
		m.VX, m.VY = 0, 0
	}
}

type Measurement struct {
	Channel int     `json:"c"`
	Visit   int     `json:"v"`
	MJD     float64 `json:"t"`
	Lambda  float64 `json:"l"`
	Flux    float64 `json:"f"`
	Err     float64 `json:"e"`
	OK      bool    `json:"ok"`
}

// Measure does forced photometry along the fitted motion in every loaded frame
// of every channel: that gives the spectrum and the light curves at once.
func (f *Field) Measure(m *Motion) []Measurement {
	var out []Measurement
	for c := range NumChannels {
		for _, fr := range f.Frames[c] {
			if !fr.Loaded || !fr.Available {
				continue
			}
			x, y := m.At(fr.MJD)
			flux, err, good, npix := aperture(fr, f.N, x, y)
			if good == 0 {
				// Nothing usable in the aperture, e.g. a fully saturated core.
				continue
			}
			ok := float64(npix-good) < 0.3*float64(npix)
			lambda := fr.LambdaAt(f.WCS.ToSky(x, y))
			out = append(out, Measurement{c, fr.Visit, fr.MJD, lambda, flux * f.FluxScale, err * f.FluxScale, ok})
		}
	}
	return out
}
