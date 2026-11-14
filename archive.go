package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"time"

	"spherex/internal/sky"
)

// Requests to the proxy run in goroutines; results are posted back to the
// render loop and dropped if the user has opened another field meanwhile.

type statusEvent struct {
	Stage string `json:"stage"` // visits or catalog
	State string `json:"state"` // loading, done, empty or error
	Error string `json:"error,omitempty"`
	Count int    `json:"count,omitempty"`
	// Missing names catalogs that could not be reached this time.
	Missing []string `json:"missing,omitempty"`
}

// cutoutSize is the side of the cutout in degrees. Cutouts come in detector
// orientation, so they must reach the corners of the north-up field at any angle.
func cutoutSize(fov float64) float64 {
	return fov / 60 * math.Sqrt2 * 1.02
}

// transientError is a failure worth retrying later: the archive was busy or
// unreachable, rather than the data being bad.
type transientError struct{ error }

func (a *App) get(path string) ([]byte, error) {
	res, err := http.Get(a.api + path)
	if err != nil {
		return nil, transientError{err}
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}
	if res.StatusCode != http.StatusOK {
		err := fmt.Errorf("HTTP %d", res.StatusCode)
		var e struct{ Error string }
		if json.Unmarshal(body, &e) == nil && e.Error != "" {
			err = errors.New(e.Error)
		}
		// The proxy answers 502 when IRSA failed, which is usually temporary.
		if res.StatusCode >= 500 {
			return nil, transientError{err}
		}
		return nil, err
	}
	return body, nil
}

func (a *App) openReal(req OpenRequest, fov float64) {
	gen := a.gen
	a.field = nil
	a.sel = nil
	a.emit("status", statusEvent{Stage: "visits", State: "loading"})
	go func() {
		body, err := a.get(fmt.Sprintf("/api/visits?ra=%.6f&dec=%.6f", req.RA, req.Dec))
		var res struct {
			Visits []sky.ArchiveImage `json:"visits"`
		}
		if err == nil {
			err = json.Unmarshal(body, &res)
		}
		a.Post(func() {
			if gen != a.gen {
				return
			}
			if err != nil {
				a.emit("status", statusEvent{Stage: "visits", State: "error", Error: err.Error()})
				return
			}
			if len(res.Visits) == 0 {
				a.emit("status", statusEvent{Stage: "visits", State: "empty"})
				return
			}
			f := sky.NewRealField(req.RA, req.Dec, fov, res.Visits, sky.TimeToMJD(time.Now()))
			if f.Name == "" {
				f.Name = req.Name
			}
			a.emit("status", statusEvent{Stage: "visits", State: "done", Count: len(res.Visits)})
			a.startField(f, req, fov)
			a.fetchCatalog(f)
		})
	}()
}

// maxAttempts bounds the retries of a cutout the archive keeps failing on.
const maxAttempts = 4

func (a *App) fetchCutout(fr *sky.Frame) {
	gen, f := a.gen, a.field
	path := fmt.Sprintf("/api/cutout?path=%s&ra=%.6f&dec=%.6f&size=%.5f",
		url.QueryEscape(fr.File), f.WCS.RA, f.WCS.Dec, cutoutSize(f.FOV))
	go func() {
		body, err := a.get(path)
		a.Post(func() {
			if gen != a.gen {
				return
			}
			l := &a.loader
			l.inflight--
			var te transientError
			if errors.As(err, &te) && l.attempts[fr] < maxAttempts {
				l.attempts[fr]++
				delay := time.Duration(10<<l.attempts[fr]) * time.Second
				l.retry = append(l.retry, retryItem{fr, time.Now().Add(delay)})
				return
			}
			if err == nil {
				err = f.LoadCutout(fr, body)
			}
			if err != nil {
				// A frame that cannot be read is shown as missing, like a gap in coverage.
				println("cutout", fr.File, err.Error())
				fr.Loaded, fr.Available = true, false
			}
			var ev loadedEvent
			a.frameLoaded(fr, &ev)
			a.emit("loaded", ev)
		})
	}()
}

func (a *App) fetchCatalog(f *sky.Field) {
	gen := a.gen
	a.emit("status", statusEvent{Stage: "catalog", State: "loading"})
	go func() {
		body, err := a.get(fmt.Sprintf("/api/catalog?ra=%.6f&dec=%.6f", f.WCS.RA, f.WCS.Dec))
		var res struct {
			Sources []sky.CatalogEntry `json:"sources"`
			Missing []string           `json:"missing"`
		}
		if err == nil {
			err = json.Unmarshal(body, &res)
		}
		a.Post(func() {
			if gen != a.gen {
				return
			}
			if err != nil {
				a.emit("status", statusEvent{Stage: "catalog", State: "error", Error: err.Error()})
				return
			}
			f.SetCatalog(res.Sources)
			a.selDirty = true
			a.emit("status", statusEvent{Stage: "catalog", State: "done", Count: len(res.Sources), Missing: res.Missing})
		})
	}()
}
