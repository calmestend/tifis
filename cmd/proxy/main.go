// Command proxy serves the web app and relays the SPHEREx archive at IRSA,
// which does not send CORS headers, so browsers cannot query it directly.
//
//	GET /api/visits?ra=&dec=              spectral images covering a position (JSON)
//	GET /api/cutout?path=&ra=&dec=&size=  cutout without the PSF cube (FITS)
//	GET /api/catalog?ra=&dec=             Gaia DR3 and CatWISE2020 sources nearby (JSON)
//	GET /api/gaia/progress?ra=&dec=       Gaia bulk files being streamed for a field (JSON)
//
// SPHEREx images and CatWISE come from IRSA; Gaia DR3 comes from the bulk
// files on the ESA CDN, streamed and split into sky tiles (see internal/gaia).
//
// IRSA queries take from seconds to minutes, so answers are cached on disk and
// the challenge's test objects are fetched in the background at startup.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"spherex/internal/gaia"
	"spherex/internal/irsa"
	"spherex/internal/sky"
)

const (
	// The archive receives new images weekly, so a day-old list is fresh enough.
	visitsTTL = 24 * time.Hour
	// Covers the corners of the largest field the app offers (12').
	catalogRadius = 0.15
)

func main() {
	cacheDir, _ := os.UserCacheDir()
	addr := flag.String("addr", ":8080", "listen address")
	web := flag.String("web", "web", "directory with the built web app")
	cache := flag.String("cache", filepath.Join(cacheDir, "spherex-proxy"), "cache directory")
	base := flag.String("irsa", irsa.DefaultBase, "IRSA base URL")
	parallel := flag.Int("parallel", 8, "concurrent requests to IRSA")
	flag.Parse()

	c, err := irsa.NewCache(*cache)
	if err != nil {
		log.Fatal(err)
	}
	g, err := gaia.NewStore(filepath.Join(*cache, "gaia"))
	if err != nil {
		log.Fatal(err)
	}
	s := &server{client: irsa.NewClient(*base, *parallel), cache: c, gaia: g}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"ok": true})
	})
	mux.HandleFunc("GET /api/visits", s.visits)
	mux.HandleFunc("GET /api/cutout", s.cutout)
	mux.HandleFunc("GET /api/catalog", s.catalog)
	mux.HandleFunc("GET /api/gaia/progress", func(w http.ResponseWriter, r *http.Request) {
		ra, dec, ok := position(w, r)
		if !ok {
			return
		}
		writeJSON(w, s.gaia.Progress(ra, dec, catalogRadius))
	})
	mux.Handle("GET /", noCache(http.FileServer(http.Dir(*web))))

	go s.warm()
	log.Printf("serving %s and the IRSA proxy on %s (cache %s)", *web, *addr, *cache)
	srv := &http.Server{Addr: *addr, Handler: logRequests(mux), ReadHeaderTimeout: 10 * time.Second}
	log.Fatal(srv.ListenAndServe())
}

type server struct {
	client *irsa.Client
	cache  *irsa.Cache
	gaia   *gaia.Store
}

func (s *server) visits(w http.ResponseWriter, r *http.Request) {
	ra, dec, ok := position(w, r)
	if !ok {
		return
	}
	data, hit, err := s.visitsData(ra, dec)
	writeCached(w, data, hit, err)
}

func (s *server) catalog(w http.ResponseWriter, r *http.Request) {
	ra, dec, ok := position(w, r)
	if !ok {
		return
	}
	data, hit, err := s.catalogData(ra, dec)
	writeCached(w, data, hit, err)
}

// round to ~2" so nearby requests, and the warm-up, share the cache.
func round(ra, dec float64) (float64, float64) {
	return math.Round(ra*2000) / 2000, math.Round(dec*2000) / 2000
}

func (s *server) visitsData(ra, dec float64) ([]byte, bool, error) {
	ra, dec = round(ra, dec)
	return s.cache.Get(fmt.Sprintf("visits %.4f %.4f", ra, dec), visitsTTL, func() ([]byte, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
		defer cancel()
		// SIA answers in seconds; TAP is slower but sees data ingested in the
		// last day, and is a second way in when SIA is down.
		source := "SIA"
		v, err := s.client.VisitsSIA(ctx, ra, dec)
		if err != nil {
			log.Printf("SIA failed, trying TAP: %v", err)
			source = "TAP"
			if v, err = s.client.Visits(ctx, ra, dec); err != nil {
				return nil, err
			}
		}
		return json.Marshal(map[string]any{"ra": ra, "dec": dec, "source": source, "fetched": time.Now().UTC(), "visits": v})
	})
}

// catalogData merges Gaia DR3 (from the ESA CDN) and CatWISE2020 (from IRSA).
// Catalogs do not change, so complete answers are cached for good; when
// CatWISE fails the Gaia part is still returned, marked as partial.
func (s *server) catalogData(ra, dec float64) ([]byte, bool, error) {
	ra, dec = round(ra, dec)
	key := fmt.Sprintf("catalog %.4f %.4f %.3f gaia-cdn", ra, dec, catalogRadius)
	if data, ok := s.cache.Lookup(key, 0); ok {
		return data, true, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()

	// CatWISE has its own cache and keeps going in the background: once Gaia
	// is ready it gets a short grace period, and if IRSA is slow the answer
	// goes out without it and the next request picks it up.
	var cw []sky.CatalogEntry
	var cwErr error
	cwDone := make(chan struct{})
	go func() {
		defer close(cwDone)
		data, _, err := s.cache.Get(fmt.Sprintf("catwise %.4f %.4f %.3f", ra, dec, catalogRadius), 0, func() ([]byte, error) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			defer cancel()
			entries, err := s.client.CatWISE(ctx, ra, dec, catalogRadius)
			if err != nil {
				return nil, err
			}
			return json.Marshal(entries)
		})
		if err == nil {
			err = json.Unmarshal(data, &cw)
		}
		cwErr = err
	}()
	stars, err := s.gaia.Cone(ctx, ra, dec, catalogRadius)
	if err != nil {
		return nil, false, err
	}
	select {
	case <-cwDone:
	case <-time.After(20 * time.Second):
		cw, cwErr = nil, errors.New("still waiting for IRSA")
	}

	var entries []sky.CatalogEntry
	for _, st := range stars {
		if !(st.G < 19) {
			continue
		}
		pmra, pmdec := float64(st.PMRA), float64(st.PMDec)
		if math.IsNaN(pmra) || math.IsNaN(pmdec) {
			pmra, pmdec = 0, 0
		}
		entries = append(entries, sky.CatalogEntry{
			Catalog: "Gaia DR3", Name: fmt.Sprintf("Gaia DR3 %d", st.SourceID),
			RA: st.RA, Dec: st.Dec, PMRA: pmra, PMDec: pmdec, MJD: gaia.EpochMJD, Mag: float64(st.G),
		})
	}
	res := map[string]any{"ra": ra, "dec": dec, "radius": catalogRadius, "sources": irsa.Merge(entries, cw)}
	if cwErr != nil {
		log.Printf("catalog %.4f %.4f without CatWISE: %v", ra, dec, cwErr)
		res["missing"] = []string{"CatWISE2020"}
	}
	data, err := json.Marshal(res)
	if err == nil && cwErr == nil {
		s.cache.Put(key, data)
	}
	return data, false, err
}

// warm fetches the test objects ahead of time, at the positions the app asks
// for, so the first visitor does not wait minutes for IRSA. It goes one query
// at a time to leave the archive (and the app's own requests) room.
func (s *server) warm() {
	now := sky.TimeToMJD(time.Now())
	for _, step := range []struct {
		name  string
		fetch func(float64, float64) ([]byte, bool, error)
	}{{"visits", s.visitsData}, {"catalog", s.catalogData}} {
		for _, t := range sky.Targets {
			ra, dec := t.Source.Position(now)
			start := time.Now()
			if _, hit, err := step.fetch(ra, dec); err != nil {
				log.Printf("warm-up %s %s: %v", t.ID, step.name, err)
			} else if !hit {
				log.Printf("warm-up %s %s: fetched in %s", t.ID, step.name, time.Since(start).Round(time.Second))
			}
		}
	}
}

func writeCached(w http.ResponseWriter, data []byte, hit bool, err error) {
	if err != nil {
		httpError(w, http.StatusBadGateway, err)
		return
	}
	w.Header().Set("X-Cache", cacheState(hit))
	w.Header().Set("Content-Type", "application/json")
	w.Write(data)
}

func (s *server) cutout(w http.ResponseWriter, r *http.Request) {
	ra, dec, ok := position(w, r)
	if !ok {
		return
	}
	path := r.URL.Query().Get("path")
	size, err := strconv.ParseFloat(r.URL.Query().Get("size"), 64)
	switch {
	case !irsa.ValidPath(path):
		httpError(w, http.StatusBadRequest, fmt.Errorf("path must be a SPHEREx archive file"))
		return
	case err != nil || size <= 0 || size > 0.3:
		httpError(w, http.StatusBadRequest, fmt.Errorf("size must be in (0, 0.3] degrees"))
		return
	}
	key := fmt.Sprintf("cutout %s %.6f %.6f %.5f", path, ra, dec, size)
	data, hit, err := s.cache.Get(key, 0, func() ([]byte, error) {
		// If the tab goes away while this waits for a slot, it gives up; once
		// the download has started it finishes and lands in the cache.
		return s.client.Cutout(r.Context(), path, ra, dec, size)
	})
	if err != nil {
		httpError(w, http.StatusBadGateway, err)
		return
	}
	// Archive paths carry their processing version, so a cutout never changes.
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	w.Header().Set("X-Cache", cacheState(hit))
	w.Header().Set("Content-Type", "application/fits")
	w.Write(data)
}

func position(w http.ResponseWriter, r *http.Request) (ra, dec float64, ok bool) {
	ra, err1 := strconv.ParseFloat(r.URL.Query().Get("ra"), 64)
	dec, err2 := strconv.ParseFloat(r.URL.Query().Get("dec"), 64)
	if err1 != nil || err2 != nil || ra < 0 || ra >= 360 || dec < -90 || dec > 90 {
		httpError(w, http.StatusBadRequest, fmt.Errorf("ra must be in [0, 360) and dec in [-90, 90] degrees"))
		return 0, 0, false
	}
	return ra, dec, true
}

func cacheState(hit bool) string {
	if hit {
		return "HIT"
	}
	return "MISS"
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func httpError(w http.ResponseWriter, code int, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}

// noCache makes the browser revalidate the app files, so a rebuild shows up on reload.
func noCache(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		h.ServeHTTP(w, r)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func logRequests(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		api := strings.HasPrefix(r.URL.Path, "/api/")
		if api {
			w.Header().Set("Access-Control-Allow-Origin", "*")
		}
		h.ServeHTTP(rec, r)
		if api || rec.status >= 400 {
			log.Printf("%s %s %d %s", r.Method, r.URL.RequestURI(), rec.status, time.Since(start).Round(time.Millisecond))
		}
	})
}
