package gaia

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"math"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fakeCDN serves one gzip CSV shaped like a Gaia bulk file, with its listing.
func fakeCDN(t *testing.T, stars []Star) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	sort.Slice(stars, func(i, j int) bool { return stars[i].SourceID < stars[j].SourceID })
	var csv strings.Builder
	csv.WriteString("# %ECSV 1.0\n# ---\n")
	csv.WriteString("solution_id,designation,source_id,random_index,ref_epoch,ra,ra_error,dec,dec_error,parallax,parallax_error,pm,pmra,pmra_error,pmdec,phot_g_mean_mag,extra\n")
	for _, s := range stars {
		pmra := fmt.Sprint(s.PMRA)
		if s.PMRA != s.PMRA {
			pmra = "null"
		}
		fmt.Fprintf(&csv, "1,\"Gaia DR3 %d\",%d,7,2016.0,%.10f,0.1,%.10f,0.1,%g,0.1,1,%s,0.1,%g,%g,x\n",
			s.SourceID, s.SourceID, s.RA, s.Dec, s.Parallax, pmra, s.PMDec, s.G)
	}
	var gz bytes.Buffer
	w := gzip.NewWriter(&gz)
	w.Write([]byte(csv.String()))
	w.Close()
	lo, hi := PixelOf(stars[0].SourceID), PixelOf(stars[len(stars)-1].SourceID)
	name := fmt.Sprintf("GaiaSource_%06d-%06d.csv.gz", lo, hi)

	var ranges atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/list":
			fmt.Fprintf(rw, `<ListBucketResult><IsTruncated>false</IsTruncated><Contents><Key>Gaia/gdr3/gaia_source/%s</Key><Size>%d</Size></Contents><Contents><Key>Gaia/gdr3/gaia_source/_MD5SUM.txt</Key><Size>1</Size></Contents></ListBucketResult>`, name, gz.Len())
		case r.URL.Path == "/files/"+name:
			ranges.Add(1)
			http.ServeContent(rw, r, name, time.Time{}, bytes.NewReader(gz.Bytes()))
		default:
			http.NotFound(rw, r)
		}
	}))
	return srv, &ranges
}

func TestConeStreamsTilesOnce(t *testing.T) {
	ra0, dec0 := 45.1, 0.2
	r := rand.New(rand.NewPCG(1, 2))
	var stars []Star
	for i := range 3000 {
		ra := ra0 + (r.Float64()*2-1)*0.4
		dec := dec0 + (r.Float64()*2-1)*0.4
		id := Ang2Pix(12, ra, dec)<<35 + int64(i)
		pm := float32(r.NormFloat64() * 10)
		if i%7 == 0 {
			pm = float32(math.NaN())
		}
		stars = append(stars, Star{SourceID: id, RA: ra, Dec: dec, Parallax: 1.5, PMRA: pm, PMDec: -3, G: float32(10 + r.Float64()*10)})
	}
	srv, ranges := fakeCDN(t, stars)
	defer srv.Close()

	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s.Base, s.ListURL = srv.URL+"/files/", srv.URL+"/list?prefix=x&delimiter=/"
	s.Chunk = 4096 // many chunks, to exercise the ordered parallel reader

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	got, err := s.Cone(ctx, ra0, dec0, 0.15)
	if err != nil {
		t.Fatal(err)
	}
	want := 0
	for _, st := range stars {
		if separation(ra0, dec0, st.RA, st.Dec) <= 0.15 {
			want++
		}
	}
	if len(got) != want || want == 0 {
		t.Fatalf("got %d sources, want %d", len(got), want)
	}
	nulls := 0
	for _, st := range got {
		if st.PMRA != st.PMRA {
			nulls++
		}
		if st.Parallax != 1.5 || st.PMDec != -3 {
			t.Fatalf("columns mixed up: %+v", st)
		}
	}
	if nulls == 0 {
		t.Error("null proper motions should read as NaN")
	}

	// Wait for the background stream to finish the file, then ask again.
	for !s.fileDone(fmt.Sprintf("GaiaSource_%06d-%06d.csv.gz", PixelOf(stars[0].SourceID), PixelOf(stars[len(stars)-1].SourceID))) {
		time.Sleep(10 * time.Millisecond)
	}
	before := ranges.Load()
	again, err := s.Cone(ctx, ra0+0.2, dec0, 0.15)
	if err != nil || len(again) == 0 {
		t.Fatalf("second cone: %d sources, %v", len(again), err)
	}
	if ranges.Load() != before {
		t.Errorf("second cone downloaded again (%d range requests)", ranges.Load()-before)
	}
}

// GAIA_LIVE=1 streams the real file around Barnard's Star from the ESA CDN.
func TestLiveBarnard(t *testing.T) {
	if os.Getenv("GAIA_LIVE") == "" {
		t.Skip("GAIA_LIVE not set")
	}
	dir := os.Getenv("GAIA_DIR")
	if dir == "" {
		dir = t.TempDir()
	}
	s, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	stars, err := s.Cone(context.Background(), 269.446, 4.7707, 0.15)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%d sources in %s", len(stars), time.Since(start).Round(time.Second))
	for _, st := range stars {
		if st.SourceID == 4472832130942575872 {
			t.Logf("Barnard's Star: %+v", st)
			return
		}
	}
	t.Error("Barnard's Star not found")
}
