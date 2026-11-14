package sky

import (
	"math"
	"os"
	"testing"
)

func TestRealFieldGroupsExposures(t *testing.T) {
	images := []ArchiveImage{
		{Path: "a1", MJD: 60902.10, Detector: 1, Exposure: "A"},
		{Path: "a4", MJD: 60902.11, Detector: 4, Exposure: "A"},
		{Path: "b3", MJD: 60905.00, Detector: 3, Exposure: "B"},
		{Path: "b6", MJD: 60905.00, Detector: 6, Exposure: "B"},
		{Path: "c2", MJD: 61090.00, Detector: 2, Exposure: "C"},
	}
	f := NewRealField(10, 10, 6, images, testNow)
	if len(f.Visits) != 3 {
		t.Fatalf("want 3 visits, got %d", len(f.Visits))
	}
	if f.Visits[1].Season != 0 || f.Visits[2].Season != 1 {
		t.Errorf("seasons %d %d; a six-month gap should start a new one", f.Visits[1].Season, f.Visits[2].Season)
	}
	if !f.Frames[0][0].Available || !f.Frames[3][0].Available || f.Frames[1][0].Available {
		t.Error("exposure A should cover detectors 1 and 4 only")
	}
	if f.Frames[0][0].File != "a1" || f.Visits[0].SubBand != -1 {
		t.Errorf("frame path %q, sub-band %v", f.Frames[0][0].File, f.Visits[0].SubBand)
	}
}

// The fixture is a real DR1 cutout of Barnard's Star in detector 6 (see
// internal/wcs/testdata).
func TestLoadRealCutout(t *testing.T) {
	data, err := os.ReadFile("../wcs/testdata/barnard_d6.fits")
	if err != nil {
		t.Fatal(err)
	}
	barnard := Targets[0].Source
	ra, dec := barnard.Position(testNow)
	f := NewRealField(ra, dec, 6, []ArchiveImage{
		{Path: "ibe/data/spherex/x/level2_2025W33_2C_0321_1D6_spx.fits", MJD: 60902.8647, Detector: 6, Exposure: "2025W33_2C_0321_1"},
	}, testNow)
	if f.TargetID != "barnard" || len(f.Visits) != 1 || !f.Frames[5][0].Available || f.Frames[2][0].Available {
		t.Fatalf("unexpected plan: target %q, %d visits", f.TargetID, len(f.Visits))
	}
	fr := f.Frames[5][0]
	if err := f.LoadCutout(fr, data); err != nil {
		t.Fatal(err)
	}
	if math.Abs(fr.Lambda-4.93) > 0.02 {
		t.Errorf("wavelength at center %.3f µm, want about 4.93", fr.Lambda)
	}
	if sb := f.Visits[0].SubBand; sb < 14 || sb > 16.5 {
		t.Errorf("sub-band %.1f, want near the long end of detector 6", sb)
	}

	// The star should be where its proper motion puts it on the observation date.
	sra, sdec := barnard.Position(fr.MJD)
	x, y := f.WCS.ToPixel(sra, sdec)
	cx, cy, snr := Centroid(fr, f.N, x, y, 2)
	t.Logf("predicted (%.2f, %.2f), centroid (%.2f, %.2f), SNR %.0f", x, y, cx, cy, snr)
	if snr < 20 || math.Hypot(cx-x, cy-y) > 0.7 {
		t.Errorf("Barnard's Star not found at its predicted position")
	}

	// Its core is saturated and flagged, so the measurement must say so.
	flux, ferr, ok := Aperture(fr, f.N, cx, cy)
	t.Logf("flux %.1f ± %.2f %s at %.3f µm, reliable %v", flux*f.FluxScale, ferr*f.FluxScale, f.FluxUnit, fr.LambdaAt(f.WCS.ToSky(cx, cy)), ok)
	if ok || flux <= 0 || ferr <= 0 {
		t.Errorf("saturated core should give a positive flux marked unreliable")
	}
}
