package irsa

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"spherex/internal/fits"
)

const tapCSV = `uri,time_bounds_lower,energy_bandpassname
ibe/data/spherex/qr3/level2/2025W33_2C/l2b-v27-2026-228/6/level2_2025W33_2C_0321_1D6_spx_l2b-v27-2026-228.fits,60902.864715,SPHEREx-D6
ibe/data/spherex/dr1/level2/2025W33_2C/l2b-v27-2026-228/6/level2_2025W33_2C_0321_1D6_spx_l2b-v27-2026-228.fits,60902.864715,SPHEREx-D6
ibe/data/spherex/dr1/level2/2025W33_2C/l2b-v27-2026-228/3/level2_2025W33_2C_0321_1D3_spx_l2b-v27-2026-228.fits,60902.864715,SPHEREx-D3
ibe/data/spherex/dr1/level2/2025W21_2C/l2b-v27-2026-199/2/level2_2025W21_2C_0401_4D2_spx_l2b-v27-2026-199.fits,60800.5,SPHEREx-D2
`

func TestParseVisits(t *testing.T) {
	v, err := parseVisits([]byte(tapCSV))
	if err != nil {
		t.Fatal(err)
	}
	if len(v) != 3 {
		t.Fatalf("want 3 visits after dropping the duplicate, got %d", len(v))
	}
	if v[0].Detector != 2 || v[0].Exposure != "2025W21_2C_0401_4" {
		t.Errorf("first visit %+v", v[0])
	}
	for _, x := range v {
		if x.Detector == 6 && x.Release != "dr1" {
			t.Errorf("duplicate should resolve to dr1, got %s", x.Release)
		}
	}
	if _, err := parseVisits([]byte("<VOTABLE>error</VOTABLE>")); err == nil {
		t.Error("expected an error for a non-CSV answer")
	}
}

func TestValidPath(t *testing.T) {
	for p, want := range map[string]bool{
		"ibe/data/spherex/dr1/level2/x/1/level2_x.fits": true,
		"ibe/data/spherex/../../etc/passwd.fits":        false,
		"ibe/data/wise/allwise/x.fits":                  false,
		"ibe/data/spherex/x.fits?center=1":              false,
		"https://evil.example/ibe/data/spherex/x.fits":  false,
	} {
		if ValidPath(p) != want {
			t.Errorf("ValidPath(%q) = %v", p, !want)
		}
	}
}

func fakeCutout() []byte {
	card := func(cards ...string) []byte {
		var b strings.Builder
		for _, c := range append(cards, "END") {
			fmt.Fprintf(&b, "%-80s", c)
		}
		s := b.String()
		return []byte(s + strings.Repeat(" ", (len(s)+2879)/2880*2880-len(s)))
	}
	img := func(name string, n int) fits.HDU {
		return fits.HDU{
			Header: card("XTENSION= 'IMAGE   '", "BITPIX  =                    8", "NAXIS   =                    1",
				fmt.Sprintf("NAXIS1  = %20d", n), fmt.Sprintf("EXTNAME = '%s'", name)),
			Data: make([]byte, (n+2879)/2880*2880),
		}
	}
	return fits.Join([]fits.HDU{
		{Header: card("SIMPLE  =                    T", "BITPIX  =                    8", "NAXIS   =                    0")},
		img("IMAGE", 100), img("FLAGS", 100), img("EPSF", 50000), img("WCS-WAVE", 720),
	})
}

func TestCutoutDropsPSFAndRetries(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			http.Error(w, "busy", http.StatusServiceUnavailable)
			return
		}
		if r.URL.Query().Get("center") != "269.446080,4.770690d" {
			t.Errorf("center = %q", r.URL.Query().Get("center"))
		}
		w.Write(fakeCutout())
	}))
	defer srv.Close()

	c := NewClient(srv.URL, 2)
	data, err := c.Cutout(context.Background(), "ibe/data/spherex/dr1/level2/x/6/level2_x.fits", 269.44608, 4.77069, 0.1)
	if err != nil {
		t.Fatal(err)
	}
	hdus, err := fits.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, h := range hdus {
		names = append(names, h.Name())
	}
	if got := strings.Join(names, ","); got != "PRIMARY,IMAGE,FLAGS,WCS-WAVE" {
		t.Errorf("kept %s", got)
	}
	if calls.Load() != 2 {
		t.Errorf("expected one retry, got %d calls", calls.Load())
	}
}

func TestCutoutDoesNotRetryClientErrors(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		http.NotFound(w, nil)
	}))
	defer srv.Close()
	_, err := NewClient(srv.URL, 2).Cutout(context.Background(), "ibe/data/spherex/x.fits", 1, 1, 0.1)
	if err == nil || calls.Load() != 1 {
		t.Fatalf("err=%v calls=%d", err, calls.Load())
	}
}

func TestCacheMergesConcurrentRequests(t *testing.T) {
	c, err := NewCache(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var fetches atomic.Int32
	release := make(chan struct{})
	fetch := func() ([]byte, error) {
		fetches.Add(1)
		<-release
		return []byte("visits"), nil
	}
	var wg sync.WaitGroup
	for range 5 {
		wg.Go(func() {
			if data, _, err := c.Get("k", time.Hour, fetch); err != nil || !bytes.Equal(data, []byte("visits")) {
				t.Errorf("got %q, %v", data, err)
			}
		})
	}
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()
	if fetches.Load() != 1 {
		t.Errorf("fetched %d times", fetches.Load())
	}
	if _, hit, _ := c.Get("k", time.Hour, fetch); !hit {
		t.Error("second read should hit the disk cache")
	}
	if _, hit, _ := c.Get("k", time.Nanosecond, func() ([]byte, error) { return []byte("new"), nil }); hit {
		t.Error("expired entry should be fetched again")
	}
}
