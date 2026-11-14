package sky

import (
	"math"
	"testing"
)

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
