package sky

import (
	"fmt"
	"math"
	"math/rand/v2"
	"sort"
)

const (
	NumChannels = 6
	// SubBands per detector: the linear variable filters split each detector
	// into 17 spectral channels, so every visit sees a source at a different
	// wavelength.
	SubBands = 17
	// PixScale in arcsec per pixel.
	PixScale = 6.15
	// Saturation in image units; pixels above it are flagged NONLINEAR.
	Saturation = 800

	surveyStartMJD = 60796 // 2025-05-01
	processingDays = 7
	starBoxArcmin  = 13
)

type Channel struct {
	Lo, Hi float64 // µm
	Sigma  float64 // pixel noise, image units
	FWHM   float64 // pixels
	Zodi   float64 // typical background, image units
}

var Channels = [NumChannels]Channel{
	{0.75, 1.10, 0.004, 1.7, 0.12},
	{1.10, 1.60, 0.004, 1.7, 0.10},
	{1.60, 2.40, 0.004, 1.8, 0.07},
	{2.40, 3.80, 0.005, 1.9, 0.05},
	{3.80, 4.40, 0.008, 2.0, 0.09},
	{4.40, 5.00, 0.010, 2.1, 0.14},
}

func (c Channel) Lambda(sub float64) float64 {
	return c.Lo + (sub+0.5)/SubBands*(c.Hi-c.Lo)
}

// Flag is the SPHEREx FLAGS bitmask; bit n is named by keyword MSKNnnnn in the
// FLAGS header of every spectral image.
type Flag uint32

var flagBits = map[string]uint{
	"TRANSIENT": 0, "OVERFLOW": 1, "SUR_ERROR": 2, "NONFUNC": 6, "DICHROIC": 7,
	"MISSING_DATA": 9, "HOT": 10, "COLD": 11, "FULLSAMPLE": 12, "PHANMISS": 14,
	"NONLINEAR": 15, "PERSIST": 17, "OUTLIER": 19, "CROSSTALK": 20, "SOURCE": 21,
	"GHOST": 22, "GHOST_FPA": 23, "GHOST_EXT": 24, "STREAK": 25, "BLOOM": 26,
	"SNOWBALL": 27, "HALO": 28,
}

const (
	FlagTransient Flag = 1 << 0
	FlagMissing   Flag = 1 << 9
	FlagHot       Flag = 1 << 10
	FlagNonlinear Flag = 1 << 15
)

// BadMask holds the flags that make a pixel unreliable for photometry: the list
// the SPHEREx Source Discovery Tool uses for its CONTAINS_BAD_PIXEL warning.
// SOURCE, FULLSAMPLE, PHANMISS and OUTLIER are informational and not included.
var BadMask = func() Flag {
	var m Flag
	for _, n := range []string{"TRANSIENT", "OVERFLOW", "SUR_ERROR", "NONFUNC", "DICHROIC",
		"MISSING_DATA", "HOT", "COLD", "NONLINEAR", "CROSSTALK", "GHOST", "GHOST_FPA",
		"GHOST_EXT", "STREAK", "PERSIST", "BLOOM", "SNOWBALL", "HALO"} {
		m |= 1 << flagBits[n]
	}
	return m
}()

// Bad reports whether a pixel should be left out of measurements. Saturated
// pixels still carry signal, so callers that want them can mask NONLINEAR out.
func (f Flag) Bad() bool { return f&BadMask != 0 }

// Names lists the problem flags set in f.
func (f Flag) Names() []string {
	var out []string
	for name, bit := range flagBits {
		if f&BadMask&(1<<bit) != 0 {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// Visit is one pointing that covers the field; the six detectors observe it at
// the same time, each through the same position of its filter.
type Visit struct {
	MJD     float64
	Season  int
	SubBand float64
}

type Frame struct {
	Channel, Visit int
	MJD            float64
	Lambda         float64 // µm at the field center
	Available      bool
	Loaded         bool
	File           string // archive path for real data
	Image          []float32
	Variance       []float32 // nil for simulated frames
	Flags          []Flag
	Background     float64
	Sigma          float64
	// waveAt gives the wavelength at a sky position; with the linear variable
	// filters it changes across the frame. nil means Lambda everywhere.
	waveAt func(ra, dec float64) float64
}

// LambdaAt is the wavelength the frame measured at a sky position.
func (fr *Frame) LambdaAt(ra, dec float64) float64 {
	if fr.waveAt == nil {
		return fr.Lambda
	}
	return fr.waveAt(ra, dec)
}

type Field struct {
	Name     string
	TargetID string
	WCS      WCS
	N        int
	FOV      float64 // arcmin
	Visits   []Visit
	Frames   [NumChannels][]*Frame
	Sources  []Source
	Target   int // index in Sources, or -1
	EclLat   float64
	Real     bool
	// FluxScale turns an aperture sum of pixel values into FluxUnit.
	FluxScale float64
	FluxUnit  string
	seed      uint64
}

// NewField plans a simulated cutout: which visits cover the position, at which
// wavelengths, and what is in the sky. Pixels are produced later by Load.
func NewField(ra, dec, fovArcmin, nowMJD float64) *Field {
	n := max(16, int(math.Round(fovArcmin*60/PixScale)))
	f := &Field{
		WCS:       WCS{RA: ra, Dec: dec, Scale: PixScale, CX: float64(n) / 2, CY: float64(n) / 2},
		N:         n,
		FOV:       fovArcmin,
		Target:    -1,
		FluxScale: 1,
		FluxUnit:  "",
		seed:      hashString(fmt.Sprintf("%.4f %.4f", ra, dec)),
	}
	r := newRand(f.seed)
	lon, lat := Ecliptic(ra, dec)
	f.EclLat = lat
	f.Visits = schedule(lon, lat, nowMJD, r)

	for c := range NumChannels {
		f.Frames[c] = make([]*Frame, len(f.Visits))
		for v, vis := range f.Visits {
			t := MJDToTime(vis.MJD)
			year, week := t.ISOWeek()
			f.Frames[c][v] = &Frame{
				Channel:   c,
				Visit:     v,
				MJD:       vis.MJD,
				Lambda:    Channels[c].Lambda(vis.SubBand),
				Available: r.Float64() > 0.04,
				File: fmt.Sprintf("level2_%dW%02d_%dB_%04d_1D%d_spx_l2b-v20-%d-%03d.fits",
					year, week, 1+v%2, 1+(f.seed>>uint(v%40))%9000, c+1, year, t.YearDay()),
			}
		}
	}

	mid := nowMJD
	if len(f.Visits) > 0 {
		mid = f.Visits[len(f.Visits)/2].MJD
	}
	for _, t := range Targets {
		s := t.Source
		sra, sdec := s.Position(mid)
		if Separation(ra, dec, sra, sdec) < fovArcmin/60 {
			f.Target = len(f.Sources)
			f.TargetID = t.ID
			f.Name = t.Name
			f.Sources = append(f.Sources, s)
			break
		}
	}
	f.Sources = append(f.Sources, fieldStars(f.WCS, r)...)
	return f
}

// schedule follows the survey geometry: a position is observed when the Sun is
// about 90 degrees away in ecliptic longitude, twice a year, and near the
// ecliptic poles it is observed almost continuously.
func schedule(lon, lat, nowMJD float64, r *rand.Rand) []Visit {
	s := math.Abs(math.Sin(lat * deg))
	perSeason := 17 + int(math.Round(7*math.Pow(s, 4)))
	window := 2 + 10*s*s
	if math.Abs(lat) > 78 {
		window = 150
	}
	end := nowMJD - processingDays

	var visits []Visit
	t := float64(surveyStartMJD) - 200
	season := 0
	for t < end+window {
		phase := math.Mod(SunLongitude(t)-lon-90+720, 180)
		t += (180 - phase) / 0.9856474
		lo, hi := t-window/2, t+window/2
		if hi < surveyStartMJD || lo > end {
			t += 1
			continue
		}
		perm := r.Perm(SubBands)
		added := 0
		for j := range perSeason {
			m := lo + window*(float64(j)+0.2+0.6*r.Float64())/float64(perSeason)
			if m < surveyStartMJD || m > end {
				continue
			}
			sub := float64(perm[j%SubBands]) + 0.6*r.Float64() - 0.3
			visits = append(visits, Visit{MJD: m, Season: season, SubBand: math.Max(0, math.Min(SubBands-1, sub))})
			added++
		}
		if added > 0 {
			season++
		}
		t += 1
	}
	sort.Slice(visits, func(i, j int) bool { return visits[i].MJD < visits[j].MJD })
	return visits
}

func fieldStars(w WCS, r *rand.Rand) []Source {
	b := math.Abs(GalacticLatitude(w.RA, w.Dec))
	density := 1.2 + 6*math.Exp(-b/12) // stars per arcmin² to K≈17.5
	count := int(density * starBoxArcmin * starBoxArcmin)
	half := starBoxArcmin * 60 / w.Scale / 2
	stars := make([]Source, 0, count)
	for range count {
		x := w.CX + (r.Float64()*2-1)*half
		y := w.CY + (r.Float64()*2-1)*half
		ra, dec := w.ToSky(x, y)
		mag := math.Max(6, 17.5+math.Log10(1-r.Float64())/0.32)
		u := r.Float64()
		stars = append(stars, Source{
			RA: ra, Dec: dec,
			PMRA: r.NormFloat64() * 6, PMDec: r.NormFloat64() * 6,
			RefMJD: MJDJ2000, Kind: SEDStar, Temp: 2800 + 7000*u*u,
			Flux: fluxFromMag(mag), RefLambda: 2.2,
			Catalog: mag < 16,
		})
	}
	return stars
}

// Load produces the pixels of a frame. With real data this is where the cutout
// is downloaded and its IMAGE and FLAGS layers decoded.
func (f *Field) Load(fr *Frame) {
	if fr.Loaded {
		return
	}
	defer func() { fr.Loaded = true }()
	if !fr.Available {
		return
	}
	n := f.N
	r := newRand(f.seed ^ uint64(fr.Channel+1)*0x9e3779b97f4a7c15 ^ uint64(fr.Visit+1)*0xbf58476d1ce4e5b9)
	ch := Channels[fr.Channel]
	img := make([]float32, n*n)
	flags := make([]Flag, n*n)

	lon, _ := Ecliptic(f.WCS.RA, f.WCS.Dec)
	bg := ch.Zodi * (1 + 0.15*math.Sin(2*math.Pi*fr.MJD/365.25+lon*deg)) * (1 + 0.3*(1-math.Abs(math.Sin(f.EclLat*deg))))
	gx, gy := r.NormFloat64()*bg*0.001, r.NormFloat64()*bg*0.001
	for y := range n {
		for x := range n {
			img[y*n+x] = float32(bg + gx*(float64(x)-f.WCS.CX) + gy*(float64(y)-f.WCS.CY))
		}
	}

	sigma := ch.FWHM / 2.3548
	for i := range f.Sources {
		s := &f.Sources[i]
		ra, dec := s.Position(fr.MJD)
		x, y := f.WCS.ToPixel(ra, dec)
		if x < -6 || y < -6 || x > float64(n)+6 || y > float64(n)+6 {
			continue
		}
		if flux := s.FluxAt(fr.Lambda, fr.MJD); flux > 1e-5 {
			addPSF(img, n, x, y, sigma, flux)
		}
	}

	for i, v := range img {
		if v > Saturation {
			img[i] = Saturation
			flags[i] |= FlagNonlinear
		}
		img[i] += float32(r.NormFloat64() * ch.Sigma)
	}

	for range r.IntN(n*n/4000 + 2) {
		i := r.IntN(n * n)
		img[i] += float32(0.05 + 0.4*r.Float64())
		flags[i] |= FlagHot
	}
	for range r.IntN(n*n/1500 + 2) {
		x, y := r.Float64()*float64(n), r.Float64()*float64(n)
		a := r.Float64() * 2 * math.Pi
		e := 0.3 + 3*r.Float64()
		for k := range 1 + r.IntN(4) {
			px, py := int(x+float64(k)*math.Cos(a)), int(y+float64(k)*math.Sin(a))
			if px >= 0 && py >= 0 && px < n && py < n {
				img[py*n+px] += float32(e)
				flags[py*n+px] |= FlagTransient
			}
		}
	}
	if r.Float64() < 0.05 {
		w := 2 + r.IntN(4)
		x0 := 0
		if r.IntN(2) == 1 {
			x0 = n - w
		}
		for y := range n {
			for x := x0; x < x0+w; x++ {
				img[y*n+x] = 0
				flags[y*n+x] |= FlagMissing
			}
		}
	}

	fr.Image, fr.Flags = img, flags
	fr.Background, fr.Sigma = Stats(img, flags)
}

// addPSF adds a Gaussian integrated over each pixel, so sub-pixel shifts are
// rendered faithfully.
func addPSF(img []float32, n int, x, y, sigma, flux float64) {
	rad := int(math.Ceil(4*sigma)) + 1
	x0, y0 := int(math.Floor(x))-rad, int(math.Floor(y))-rad
	k := 1 / (math.Sqrt2 * sigma)
	var ex, ey [32]float64
	size := min(2*rad+1, len(ex))
	for i := range size {
		ex[i] = 0.5 * (math.Erf((float64(x0+i+1)-x)*k) - math.Erf((float64(x0+i)-x)*k))
		ey[i] = 0.5 * (math.Erf((float64(y0+i+1)-y)*k) - math.Erf((float64(y0+i)-y)*k))
	}
	for j := range size {
		py := y0 + j
		if py < 0 || py >= n {
			continue
		}
		for i := range size {
			px := x0 + i
			if px < 0 || px >= n {
				continue
			}
			img[py*n+px] += float32(flux * ex[i] * ey[j])
		}
	}
}

// Stats returns the median and a robust sigma (scaled MAD) of unflagged pixels.
func Stats(img []float32, flags []Flag) (median, sigma float64) {
	vals := make([]float64, 0, len(img))
	for i, v := range img {
		if flags == nil || !flags[i].Bad() {
			vals = append(vals, float64(v))
		}
	}
	if len(vals) == 0 {
		return 0, 1
	}
	sort.Float64s(vals)
	median = vals[len(vals)/2]
	for i, v := range vals {
		vals[i] = math.Abs(v - median)
	}
	sort.Float64s(vals)
	return median, math.Max(1.4826*vals[len(vals)/2], 1e-6)
}

// Percentile of the unflagged pixels, p in [0, 1].
func Percentile(img []float32, flags []Flag, p float64) float64 {
	vals := make([]float64, 0, len(img))
	for i, v := range img {
		if flags == nil || !flags[i].Bad() {
			vals = append(vals, float64(v))
		}
	}
	if len(vals) == 0 {
		return 0
	}
	sort.Float64s(vals)
	return vals[min(len(vals)-1, int(p*float64(len(vals))))]
}
