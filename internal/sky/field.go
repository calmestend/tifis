// Package sky holds a field as the engine shows it: visits, frames already on
// the field grid, catalog markers and the fitted motion of a selected source.
// The server (server/) plans fields, reprojects images and measures; the
// engine composes and draws.
package sky

import (
	"encoding/binary"
	"fmt"
	"math"
	"sort"
)

const (
	NumChannels = 6
	// SubBands per detector: the linear variable filters split each detector
	// into 17 spectral channels.
	SubBands = 17
	// PixScale in arcsec per pixel.
	PixScale = 6.15
	// Aperture and annulus radii in pixels, as in server/science.py; drawn
	// around the selected source.
	ApertureRadius = 3.0
	AnnulusInner   = 7.0
	AnnulusOuter   = 16.0
)

// Channels are the wavelength ranges of the six detectors, in µm.
var Channels = [NumChannels]struct{ Lo, Hi float64 }{
	{0.75, 1.10}, {1.10, 1.60}, {1.60, 2.40}, {2.40, 3.80}, {3.80, 4.40}, {4.40, 5.00},
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
	FlagMissing   Flag = 1 << 9
	FlagNonlinear Flag = 1 << 15
)

// BadMask holds the flags that make a pixel unreliable: the list the SPHEREx
// Source Discovery Tool uses for its CONTAINS_BAD_PIXEL warning, the same as
// the server's. SOURCE, FULLSAMPLE, PHANMISS and OUTLIER are informational.
var BadMask = func() Flag {
	var m Flag
	for _, n := range []string{"TRANSIENT", "OVERFLOW", "SUR_ERROR", "NONFUNC", "DICHROIC",
		"MISSING_DATA", "HOT", "COLD", "NONLINEAR", "CROSSTALK", "GHOST", "GHOST_FPA",
		"GHOST_EXT", "STREAK", "PERSIST", "BLOOM", "SNOWBALL", "HALO"} {
		m |= 1 << flagBits[n]
	}
	return m
}()

// Bad reports whether a pixel should be left out. Saturated pixels still carry
// signal, so callers that want them can mask NONLINEAR out.
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

// Visit is one exposure covering the field.
type Visit struct {
	MJD     float64 `json:"mjd"`
	Season  int     `json:"season"`
	SubBand float64 `json:"sub"` // -1 until a frame of the visit arrives
}

type Frame struct {
	Channel, Visit int
	MJD            float64
	Lambda         float64 // µm at the field center
	Available      bool
	Loaded         bool
	File           string
	Image          []float32
	Flags          []Flag
	Background     float64
	Sigma          float64
}

type Field struct {
	Name     string
	TargetID string
	WCS      WCS
	N        int
	FOV      float64 // arcmin
	Visits   []Visit
	Frames   [NumChannels][]*Frame
	Catalog  []Source
	Target   int // index in Catalog, or -1
	Real     bool
	FluxUnit string
	// Query names the field in every request to the server.
	Query string
}

// LoadFrame decodes a frame as the server sends it, all little-endian: five
// float64 (MJD, λ at the center, background, sigma, sub-band) followed by the
// image (float32) and the flags (uint32), N×N each, north up.
func (f *Field) LoadFrame(fr *Frame, data []byte) error {
	n := f.N * f.N
	if len(data) != 40+8*n {
		return fmt.Errorf("frame of %d bytes, want %d", len(data), 40+8*n)
	}
	head := func(k int) float64 { return math.Float64frombits(binary.LittleEndian.Uint64(data[8*k:])) }
	fr.Image, fr.Flags = make([]float32, n), make([]Flag, n)
	for i := range n {
		fr.Image[i] = math.Float32frombits(binary.LittleEndian.Uint32(data[40+4*i:]))
		fr.Flags[i] = Flag(binary.LittleEndian.Uint32(data[40+4*n+4*i:]))
	}
	fr.MJD, fr.Lambda, fr.Background, fr.Sigma = head(0), head(1), head(2), head(3)
	fr.Loaded = true
	// Both detectors of a visit see the source through the same part of their
	// filters, so either frame tells which sub-band the visit is.
	if vis := &f.Visits[fr.Visit]; vis.SubBand < 0 {
		vis.SubBand = head(4)
	}
	return nil
}

// Source is a catalog entry with its position at epoch MJD.
type Source struct {
	Name  string  `json:"name"`
	RA    float64 `json:"ra"`
	Dec   float64 `json:"dec"`
	PMRA  float64 `json:"pmra"` // mas/yr, includes cos(dec)
	PMDec float64 `json:"pmdec"`
	MJD   float64 `json:"mjd"`
	Mag   float64 `json:"mag"` // Gaia G or WISE W2
}

func (s *Source) Position(mjd float64) (ra, dec float64) {
	yr := (mjd - s.MJD) / 365.25
	return s.RA + s.PMRA*yr/3.6e6/math.Cos(s.Dec*deg), s.Dec + s.PMDec*yr/3.6e6
}

type TrackPoint struct {
	Visit int     `json:"v"`
	X     float64 `json:"x"`
	Y     float64 `json:"y"`
}

// Motion is the server's linear fit of a source's position against time, in
// field pixels and days.
type Motion struct {
	X0     float64      `json:"x0"`
	Y0     float64      `json:"y0"`
	VX     float64      `json:"vx"`
	VY     float64      `json:"vy"`
	T0     float64      `json:"t0"`
	Moving bool         `json:"moving"`
	Points []TrackPoint `json:"track"`
}

func (m *Motion) At(mjd float64) (x, y float64) {
	return m.X0 + m.VX*(mjd-m.T0), m.Y0 + m.VY*(mjd-m.T0)
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
