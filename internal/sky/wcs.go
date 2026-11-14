package sky

import (
	"math"
	"time"
)

const (
	deg    = math.Pi / 180
	arcsec = deg / 3600

	MJDUnix = 40587.0
)

// WCS is a gnomonic projection with north up, east left and square pixels.
// Pixel i covers [i, i+1), so the center of the first pixel is at 0.5.
type WCS struct {
	RA, Dec float64 // tangent point, degrees
	Scale   float64 // arcsec per pixel
	CX, CY  float64 // pixel coordinates of the tangent point
}

func (w WCS) ToPixel(ra, dec float64) (x, y float64) {
	a, d := ra*deg, dec*deg
	a0, d0 := w.RA*deg, w.Dec*deg
	cosc := math.Sin(d0)*math.Sin(d) + math.Cos(d0)*math.Cos(d)*math.Cos(a-a0)
	xi := math.Cos(d) * math.Sin(a-a0) / cosc
	eta := (math.Cos(d0)*math.Sin(d) - math.Sin(d0)*math.Cos(d)*math.Cos(a-a0)) / cosc
	s := w.Scale * arcsec
	return w.CX - xi/s, w.CY - eta/s
}

func (w WCS) ToSky(x, y float64) (ra, dec float64) {
	s := w.Scale * arcsec
	xi := -(x - w.CX) * s
	eta := -(y - w.CY) * s
	rho := math.Hypot(xi, eta)
	if rho == 0 {
		return w.RA, w.Dec
	}
	a0, d0 := w.RA*deg, w.Dec*deg
	c := math.Atan(rho)
	sinc, cosc := math.Sin(c), math.Cos(c)
	d := math.Asin(cosc*math.Sin(d0) + eta*sinc*math.Cos(d0)/rho)
	a := a0 + math.Atan2(xi*sinc, rho*math.Cos(d0)*cosc-eta*math.Sin(d0)*sinc)
	return math.Mod(a/deg+360, 360), d / deg
}

func MJDToTime(mjd float64) time.Time {
	return time.Unix(0, 0).UTC().Add(time.Duration((mjd - MJDUnix) * 86400e9))
}
