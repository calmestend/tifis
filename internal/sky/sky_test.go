package sky

import (
	"math"
	"testing"
)

const testNow = 61356 // 2026-11-12

func TestWCSRoundTrip(t *testing.T) {
	w := WCS{RA: 269.45, Dec: 4.69, Scale: PixScale, CX: 30, CY: 30}
	ra, dec := w.ToSky(12.3, 47.9)
	x, y := w.ToPixel(ra, dec)
	if math.Abs(x-12.3) > 1e-6 || math.Abs(y-47.9) > 1e-6 {
		t.Fatalf("round trip gave %v %v", x, y)
	}
	// North is up (smaller y) and east is left (smaller x).
	if _, y := w.ToPixel(269.45, 4.70); y >= 30 {
		t.Errorf("north should be up, got y=%v", y)
	}
	if x, _ := w.ToPixel(269.46, 4.69); x >= 30 {
		t.Errorf("east should be left, got x=%v", x)
	}
}

func TestScheduleHasSeasons(t *testing.T) {
	src := Targets[0].Source
	ra, dec := src.Position(testNow)
	f := NewField(ra, dec, 6, testNow)
	if len(f.Visits) < 34 {
		t.Fatalf("expected at least two seasons of visits, got %d", len(f.Visits))
	}
	seasons := f.Visits[len(f.Visits)-1].Season + 1
	if seasons < 3 {
		t.Errorf("expected 3+ seasons by Nov 2026, got %d", seasons)
	}
	if f.TargetID != "barnard" {
		t.Errorf("target not found in field, got %q", f.TargetID)
	}
}

func TestTrackRecoversBarnardMotion(t *testing.T) {
	src := Targets[0].Source
	ra, dec := src.Position(testNow)
	f := NewField(ra, dec, 6, testNow)
	ch := 2
	for _, fr := range f.Frames[ch] {
		f.Load(fr)
	}
	v := 0
	for !f.Frames[ch][v].Available {
		v++
	}
	sra, sdec := src.Position(f.Visits[v].MJD)
	x, y := f.WCS.ToPixel(sra, sdec)
	m := f.Track(ch, v, x, y)
	if !m.Moving {
		t.Fatalf("Barnard's Star should be moving; %d points", len(m.Points))
	}
	pmRA, pmDec := m.ProperMotion()
	t.Logf("measured %.2f %.2f arcsec/yr from %d points", pmRA, pmDec, len(m.Points))
	if math.Abs(pmDec-10.36) > 0.8 || math.Abs(pmRA+0.80) > 0.8 {
		t.Errorf("proper motion %.2f, %.2f too far from -0.80, 10.36", pmRA, pmDec)
	}
}

func TestWISE0855IsInvisibleShortAndBrightLong(t *testing.T) {
	src := Targets[2].Source
	if r := src.FluxAt(1.0, testNow) / src.FluxAt(4.7, testNow); r > 1e-3 {
		t.Errorf("Y dwarf too bright at 1 µm: ratio %g", r)
	}
}
