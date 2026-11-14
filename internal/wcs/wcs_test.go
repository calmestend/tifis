package wcs

import (
	"encoding/json"
	"math"
	"os"
	"testing"

	"spherex/internal/fits"
)

// testdata/barnard_d6.fits is a real SPHEREx DR1 cutout around Barnard's Star
// (detector 6) without its EPSF extension; astropy_reference.json holds the
// values astropy computes for it, following the IRSA SPHEREx intro tutorial.
func load(t *testing.T) (fits.HDU, []fits.HDU) {
	t.Helper()
	data, err := os.ReadFile("testdata/barnard_d6.fits")
	if err != nil {
		t.Fatal(err)
	}
	hdus, err := fits.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	img, ok := fits.Find(hdus, "IMAGE")
	if !ok {
		t.Fatal("no IMAGE extension")
	}
	return img, hdus
}

type reference struct {
	PixelToWorld [][4]float64 `json:"pixel_to_world"`
	WorldToPixel [][4]float64 `json:"world_to_pixel"`
	Wave         [][4]float64 `json:"wave"`
}

func loadReference(t *testing.T) reference {
	t.Helper()
	data, err := os.ReadFile("testdata/astropy_reference.json")
	if err != nil {
		t.Fatal(err)
	}
	var r reference
	if err := json.Unmarshal(data, &r); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestCelestialMatchesAstropy(t *testing.T) {
	img, _ := load(t)
	ref := loadReference(t)
	c, err := NewCelestial(img)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range ref.PixelToWorld {
		ra, dec := c.PixelToSky(p[0], p[1])
		dra := (ra - p[2]) * math.Cos(dec*deg) * 3600
		if d := math.Hypot(dra, (dec-p[3])*3600); d > 0.01 {
			t.Errorf("pixel (%g, %g): off by %.4f arcsec", p[0], p[1], d)
		}
	}
	for _, p := range ref.WorldToPixel {
		x, y, ok := c.SkyToPixel(p[0], p[1])
		if !ok || math.Hypot(x-p[2], y-p[3]) > 0.001 {
			t.Errorf("sky (%g, %g): got (%.4f, %.4f), want (%.4f, %.4f)", p[0], p[1], x, y, p[2], p[3])
		}
	}
}

func TestWaveMatchesAstropy(t *testing.T) {
	img, hdus := load(t)
	ref := loadReference(t)
	w, err := NewWave(img, hdus)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range ref.Wave {
		l, bw := w.At(p[0], p[1])
		if math.Abs(l-p[2]) > 1e-5 || math.Abs(bw-p[3]) > 1e-6 {
			t.Errorf("pixel (%g, %g): got %.6f/%.6f µm, want %.6f/%.6f", p[0], p[1], l, bw, p[2], p[3])
		}
	}
}
