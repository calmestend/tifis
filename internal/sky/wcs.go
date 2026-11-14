package sky

import (
	"math"
	"time"
)

const (
	deg    = math.Pi / 180
	arcsec = deg / 3600

	MJDUnix  = 40587.0
	MJDJ2000 = 51544.5
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

// Separation returns the angular distance between two positions in degrees.
func Separation(ra1, dec1, ra2, dec2 float64) float64 {
	a := math.Sin((dec2 - dec1) * deg / 2)
	b := math.Sin((ra2 - ra1) * deg / 2)
	h := a*a + math.Cos(dec1*deg)*math.Cos(dec2*deg)*b*b
	return 2 * math.Asin(math.Sqrt(math.Min(1, h))) / deg
}

func MJDToTime(mjd float64) time.Time {
	return time.Unix(0, 0).UTC().Add(time.Duration((mjd - MJDUnix) * 86400e9))
}

func TimeToMJD(t time.Time) float64 {
	return MJDUnix + float64(t.UnixNano())/86400e9
}

// Ecliptic converts equatorial coordinates (J2000) to ecliptic longitude and latitude.
func Ecliptic(ra, dec float64) (lon, lat float64) {
	eps := 23.4393 * deg
	a, d := ra*deg, dec*deg
	b := math.Asin(math.Sin(d)*math.Cos(eps) - math.Cos(d)*math.Sin(eps)*math.Sin(a))
	l := math.Atan2(math.Sin(a)*math.Cos(eps)+math.Tan(d)*math.Sin(eps), math.Cos(a))
	return math.Mod(l/deg+360, 360), b / deg
}

// GalacticLatitude is enough to scale star density with the Milky Way.
func GalacticLatitude(ra, dec float64) float64 {
	const raNGP, decNGP = 192.85948 * deg, 27.12825 * deg
	a, d := ra*deg, dec*deg
	return math.Asin(math.Sin(d)*math.Sin(decNGP)+math.Cos(d)*math.Cos(decNGP)*math.Cos(a-raNGP)) / deg
}

// SunLongitude is the mean ecliptic longitude of the Sun, good to about 2 degrees.
func SunLongitude(mjd float64) float64 {
	return math.Mod(280.460+0.9856474*(mjd-MJDJ2000), 360)
}
