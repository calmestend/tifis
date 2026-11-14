// Package wcs implements the two coordinate systems in a SPHEREx spectral
// image: the celestial TAN-SIP projection and the WAVE-TAB lookup table that
// gives the wavelength seen by each pixel. Pixel coordinates are 0-based, with
// (0, 0) at the center of the first pixel of the first FITS row, as in astropy.
package wcs

import (
	"errors"
	"fmt"
	"math"
	"sort"

	"spherex/internal/fits"
)

const deg = math.Pi / 180

type poly struct {
	order int
	c     map[[2]int]float64
}

func (p poly) eval(u, v float64) float64 {
	s := 0.0
	for k, c := range p.c {
		s += c * math.Pow(u, float64(k[0])) * math.Pow(v, float64(k[1]))
	}
	return s
}

func readPoly(h fits.HDU, name string) poly {
	p := poly{order: h.Int(name+"_ORDER", 0), c: map[[2]int]float64{}}
	for i := 0; i <= p.order; i++ {
		for j := 0; i+j <= p.order; j++ {
			if c := h.Float(fmt.Sprintf("%s_%d_%d", name, i, j), 0); c != 0 {
				p.c[[2]int{i, j}] = c
			}
		}
	}
	return p
}

// Celestial is a TAN projection with optional SIP distortion.
type Celestial struct {
	crpix  [2]float64
	crval  [2]float64
	cd     [2][2]float64
	inv    [2][2]float64
	a, b   poly
	ap, bp poly
}

func NewCelestial(h fits.HDU) (*Celestial, error) {
	if t := h.Keyword("CTYPE1"); t != "RA---TAN" && t != "RA---TAN-SIP" {
		return nil, fmt.Errorf("wcs: unsupported projection %q", t)
	}
	c := &Celestial{
		crpix: [2]float64{h.Float("CRPIX1", 0), h.Float("CRPIX2", 0)},
		crval: [2]float64{h.Float("CRVAL1", 0), h.Float("CRVAL2", 0)},
	}
	if h.Keyword("CD1_1") != "" {
		c.cd = [2][2]float64{{h.Float("CD1_1", 0), h.Float("CD1_2", 0)}, {h.Float("CD2_1", 0), h.Float("CD2_2", 0)}}
	} else {
		d1, d2 := h.Float("CDELT1", 1), h.Float("CDELT2", 1)
		c.cd = [2][2]float64{
			{d1 * h.Float("PC1_1", 1), d1 * h.Float("PC1_2", 0)},
			{d2 * h.Float("PC2_1", 0), d2 * h.Float("PC2_2", 1)},
		}
	}
	det := c.cd[0][0]*c.cd[1][1] - c.cd[0][1]*c.cd[1][0]
	if det == 0 {
		return nil, errors.New("wcs: singular CD matrix")
	}
	c.inv = [2][2]float64{{c.cd[1][1] / det, -c.cd[0][1] / det}, {-c.cd[1][0] / det, c.cd[0][0] / det}}
	if h.Keyword("CTYPE1") == "RA---TAN-SIP" {
		c.a, c.b = readPoly(h, "A"), readPoly(h, "B")
		c.ap, c.bp = readPoly(h, "AP"), readPoly(h, "BP")
	}
	return c, nil
}

// PixelToSky converts 0-based pixel coordinates to RA and Dec in degrees.
func (c *Celestial) PixelToSky(x, y float64) (ra, dec float64) {
	u, v := x+1-c.crpix[0], y+1-c.crpix[1]
	u, v = u+c.a.eval(u, v), v+c.b.eval(u, v)
	xi := (c.cd[0][0]*u + c.cd[0][1]*v) * deg
	eta := (c.cd[1][0]*u + c.cd[1][1]*v) * deg

	a0, d0 := c.crval[0]*deg, c.crval[1]*deg
	den := math.Cos(d0) - eta*math.Sin(d0)
	ra = a0 + math.Atan2(xi, den)
	dec = math.Atan2(math.Sin(d0)+eta*math.Cos(d0), math.Hypot(xi, den))
	return math.Mod(ra/deg+360, 360), dec / deg
}

// SkyToPixel converts RA and Dec in degrees to 0-based pixel coordinates.
// ok is false for positions on the far side of the projection.
func (c *Celestial) SkyToPixel(ra, dec float64) (x, y float64, ok bool) {
	a, d := ra*deg, dec*deg
	a0, d0 := c.crval[0]*deg, c.crval[1]*deg
	cosc := math.Sin(d0)*math.Sin(d) + math.Cos(d0)*math.Cos(d)*math.Cos(a-a0)
	if cosc <= 0 {
		return 0, 0, false
	}
	xi := math.Cos(d) * math.Sin(a-a0) / cosc / deg
	eta := (math.Cos(d0)*math.Sin(d) - math.Sin(d0)*math.Cos(d)*math.Cos(a-a0)) / cosc / deg
	up := c.inv[0][0]*xi + c.inv[0][1]*eta
	vp := c.inv[1][0]*xi + c.inv[1][1]*eta

	// Start from the inverse polynomial and polish it against the forward one,
	// since AP/BP only approximate the inverse of A/B.
	u, v := up+c.ap.eval(up, vp), vp+c.bp.eval(up, vp)
	for range 3 {
		fu, fv := u+c.a.eval(u, v), v+c.b.eval(u, v)
		u, v = u-(fu-up), v-(fv-vp)
	}
	return u + c.crpix[0] - 1, v + c.crpix[1] - 1, true
}

// Wave is the WAVE-TAB spectral WCS: a grid of control points on the parent
// detector with the central wavelength and bandwidth at each one, interpolated
// bilinearly. Distortion does not apply to it.
type Wave struct {
	crpix, crval, cdelt [2]float64
	x, y                []float64 // control points, 1-based detector pixels
	values              []float64 // [wavelength, bandwidth] per control point, x fastest
}

func NewWave(image fits.HDU, hdus []fits.HDU) (*Wave, error) {
	if image.Keyword("CTYPE1W") != "WAVE-TAB" {
		return nil, errors.New("wcs: no WAVE-TAB spectral WCS")
	}
	table, ok := fits.Find(hdus, image.Keyword("PS1_0W"))
	if !ok {
		return nil, fmt.Errorf("wcs: missing %s extension", image.Keyword("PS1_0W"))
	}
	w := &Wave{
		crpix: [2]float64{image.Float("CRPIX1W", 0), image.Float("CRPIX2W", 0)},
		crval: [2]float64{image.Float("CRVAL1W", 0), image.Float("CRVAL2W", 0)},
		cdelt: [2]float64{image.Float("CDELT1W", 1), image.Float("CDELT2W", 1)},
	}
	var err error
	if w.x, err = table.Column(image.Keyword("PS1_2W")); err != nil {
		return nil, err
	}
	if w.y, err = table.Column(image.Keyword("PS2_2W")); err != nil {
		return nil, err
	}
	if w.values, err = table.Column(image.Keyword("PS1_1W")); err != nil {
		return nil, err
	}
	if len(w.values) != 2*len(w.x)*len(w.y) || len(w.x) < 2 || len(w.y) < 2 {
		return nil, errors.New("wcs: WAVE-TAB sizes do not match")
	}
	return w, nil
}

// At returns the central wavelength and bandwidth in µm seen by a 0-based pixel.
func (w *Wave) At(x, y float64) (lambda, bandwidth float64) {
	px := (x+1-w.crpix[0])*w.cdelt[0] + w.crval[0]
	py := (y+1-w.crpix[1])*w.cdelt[1] + w.crval[1]
	i, fx := cell(w.x, px)
	j, fy := cell(w.y, py)
	nx := len(w.x)
	val := func(i, j, k int) float64 { return w.values[2*(i+nx*j)+k] }
	var out [2]float64
	for k := range 2 {
		out[k] = (1-fx)*(1-fy)*val(i, j, k) + fx*(1-fy)*val(i+1, j, k) +
			(1-fx)*fy*val(i, j+1, k) + fx*fy*val(i+1, j+1, k)
	}
	return out[0], out[1]
}

// cell finds the interval of p in the increasing control points and the
// fractional position inside it, clamped to the table.
func cell(ctrl []float64, p float64) (int, float64) {
	i := sort.SearchFloat64s(ctrl, p) - 1
	i = max(0, min(i, len(ctrl)-2))
	f := (p - ctrl[i]) / (ctrl[i+1] - ctrl[i])
	return i, math.Max(0, math.Min(1, f))
}
