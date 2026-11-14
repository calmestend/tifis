package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"spherex/internal/sky"
)

// Requests to the server run in goroutines; results are posted back to the
// render loop and dropped if the user has opened another field meanwhile.

type statusEvent struct {
	Stage string `json:"stage"` // visits or catalog
	State string `json:"state"` // loading, done, empty or error
	Error string `json:"error,omitempty"`
	Count int    `json:"count,omitempty"`
	// Missing names catalogs that could not be reached this time.
	Missing []string `json:"missing,omitempty"`
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
		// The server answers 502 when IRSA failed, which is usually temporary.
		if res.StatusCode >= 500 {
			return nil, transientError{err}
		}
		return nil, err
	}
	return body, nil
}

// fieldPlan is the server's plan of a field: visits and, per channel and
// visit, the archive file of the frame ("" when there is none).
type fieldPlan struct {
	Real     bool                      `json:"real"`
	N        int                       `json:"npix"`
	Target   string                    `json:"target"`
	Name     string                    `json:"name"`
	FluxUnit string                    `json:"fluxUnit"`
	Now      int                       `json:"now"` // the day a simulated field was planned for
	Visits   []sky.Visit               `json:"visits"`
	Files    [sky.NumChannels][]string `json:"files"`
}

func (a *App) openField(req OpenRequest, fov float64) {
	gen := a.gen
	a.field = nil
	a.sel = nil
	query := fmt.Sprintf("ra=%.6f&dec=%.6f&fov=%g", req.RA, req.Dec, fov)
	if !req.Real {
		query += "&sim=1"
	}
	a.emit("status", statusEvent{Stage: "visits", State: "loading"})
	go func() {
		body, err := a.get("/api/field?" + query)
		var p fieldPlan
		if err == nil {
			err = json.Unmarshal(body, &p)
		}
		a.Post(func() {
			if gen != a.gen {
				return
			}
			if err != nil {
				a.emit("status", statusEvent{Stage: "visits", State: "error", Error: err.Error()})
				return
			}
			if len(p.Visits) == 0 {
				a.emit("status", statusEvent{Stage: "visits", State: "empty"})
				return
			}
			if !p.Real {
				// A simulated field depends on the day it was planned for.
				query += fmt.Sprintf("&now=%d", p.Now)
			}
			f := &sky.Field{
				Name: p.Name, TargetID: p.Target, N: p.N, FOV: fov, Visits: p.Visits,
				WCS:    sky.WCS{RA: req.RA, Dec: req.Dec, Scale: sky.PixScale, CX: float64(p.N) / 2, CY: float64(p.N) / 2},
				Target: -1, Real: p.Real, FluxUnit: p.FluxUnit, Query: query,
			}
			if f.Name == "" {
				f.Name = req.Name
			}
			count := 0
			for c := range sky.NumChannels {
				for v, file := range p.Files[c] {
					f.Frames[c] = append(f.Frames[c], &sky.Frame{Channel: c, Visit: v, MJD: p.Visits[v].MJD, Available: file != "", File: file})
					if file != "" {
						count++
					}
				}
			}
			a.emit("status", statusEvent{Stage: "visits", State: "done", Count: count})
			a.startField(f, req, fov)
			a.fetchCatalog(f)
		})
	}()
}

// maxAttempts bounds the retries of a frame the archive keeps failing on.
const maxAttempts = 4

func (a *App) fetchFrame(fr *sky.Frame) {
	gen, f := a.gen, a.field
	path := "/api/frame?" + f.Query
	if f.Real {
		path += "&path=" + url.QueryEscape(fr.File)
	} else {
		path += fmt.Sprintf("&c=%d&v=%d", fr.Channel, fr.Visit)
	}
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
				err = f.LoadFrame(fr, body)
			}
			if err != nil {
				// A frame that cannot be read is shown as missing, like a gap in coverage.
				println("frame", fr.File, err.Error())
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
		body, err := a.get("/api/catalog?" + f.Query)
		var res struct {
			Sources []sky.Source `json:"sources"`
			Missing []string     `json:"missing"`
			Target  int          `json:"target"`
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
			f.Catalog, f.Target = res.Sources, res.Target
			a.selDirty = true
			a.emit("status", statusEvent{Stage: "catalog", State: "done", Count: len(res.Sources), Missing: res.Missing})
		})
	}()
}

// measureSelection asks the server for the photometry and motion of the
// selected source, using the frames loaded so far; the answer goes to the
// page as it comes.
func (a *App) measureSelection() {
	gen, f, s := a.gen, a.field, a.sel
	s.busy = true
	a.selDirty = false
	a.selTimer = 1
	path := fmt.Sprintf("/api/measure?%s&band=%d&visit=%d&x=%.3f&y=%.3f", f.Query, s.Band, s.Visit, s.X, s.Y)
	go func() {
		body, err := a.get(path)
		var m sky.Motion
		if err == nil {
			err = json.Unmarshal(body, &m)
		}
		a.Post(func() {
			s.busy = false
			if gen != a.gen || a.sel != s {
				return
			}
			if err != nil {
				println("measure", err.Error())
				return
			}
			s.Motion = &m
			a.emit("select", json.RawMessage(body))
		})
	}()
}
