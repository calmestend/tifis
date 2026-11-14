package gaia

import (
	"encoding/csv"
	"os"
	"strconv"
	"testing"
)

// testdata/healpix_rows.csv holds real Gaia DR3 rows (source_id, ra, dec) from
// the start of GaiaSource_000000-003111.csv.gz plus Barnard's Star.
func TestAng2PixMatchesGaiaSourceIDs(t *testing.T) {
	f, err := os.Open("testdata/healpix_rows.csv")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	rows, err := csv.NewReader(f).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	// The pixel in a source_id comes from the position Gaia had for the source
	// when it was numbered, not exactly the DR3 one, so a few sources near
	// pixel edges (and fast movers like Barnard's Star) fall in a neighbor.
	// A wrong algorithm would miss nearly all of them.
	var miss12, miss8 int
	for _, r := range rows[1:] {
		id, _ := strconv.ParseInt(r[0], 10, 64)
		ra, _ := strconv.ParseFloat(r[1], 64)
		dec, _ := strconv.ParseFloat(r[2], 64)
		if Ang2Pix(12, ra, dec) != id>>35 {
			miss12++
		}
		if Ang2Pix(Order, ra, dec) != PixelOf(id) {
			miss8++
		}
	}
	n := len(rows) - 1
	t.Logf("%d sources: %d differ at order 12, %d at order 8", n, miss12, miss8)
	if miss12 > n/20 || miss8 > n/50 {
		t.Errorf("too many mismatches: %d and %d of %d", miss12, miss8, n)
	}
}

func TestAng2PixCoversAllFaces(t *testing.T) {
	npix := int64(12) << (2 * Order)
	seen := map[int64]bool{}
	for dec := -89.5; dec < 90; dec += 1 {
		for ra := 0.5; ra < 360; ra += 1 {
			p := Ang2Pix(Order, ra, dec)
			if p < 0 || p >= npix {
				t.Fatalf("pixel %d out of range at (%g, %g)", p, ra, dec)
			}
			seen[p>>(2*Order)] = true
		}
	}
	if len(seen) != 12 {
		t.Errorf("visited %d base faces, want 12", len(seen))
	}
}

func TestDiscPixelsIncludesCenterAndNeighbors(t *testing.T) {
	ps := DiscPixels(269.446, 4.7707, 0.2)
	center := Ang2Pix(Order, 269.446, 4.7707)
	found := false
	for _, p := range ps {
		found = found || p == center
	}
	if !found || len(ps) < 2 || len(ps) > 12 {
		t.Errorf("disc pixels %v around center %d", ps, center)
	}
}
