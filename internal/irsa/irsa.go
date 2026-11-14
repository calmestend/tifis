// Package irsa queries the SPHEREx archive at IRSA: which spectral images cover
// a position (TAP) and small cutouts of them without the PSF cube.
package irsa

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"spherex/internal/fits"
)

const DefaultBase = "https://irsa.ipac.caltech.edu"

// Visit is one spectral image (one detector, one exposure) covering a position.
type Visit struct {
	Path     string  `json:"path"` // relative to the archive root, e.g. ibe/data/spherex/...
	MJD      float64 `json:"mjd"`
	Detector int     `json:"detector"` // 1-6
	Exposure string  `json:"exposure"` // shared by the detectors of the same exposure
	Release  string  `json:"release"`  // dr1, qr3, ...
}

type Client struct {
	Base string
	HTTP *http.Client
	// Each kind of request has its own slots: slow catalog queries must not
	// hold up the visit lists the app is waiting for, nor the cutouts. TAP
	// slots are few to be gentle with the archive.
	cutouts, visits, catalogs chan struct{}
}

// NewClient limits concurrent requests to IRSA; the archive tutorials suggest
// around ten parallel cutout requests at most.
func NewClient(base string, parallel int) *Client {
	return &Client{
		Base: strings.TrimRight(base, "/"),
		// Catalog queries at IRSA can take several minutes.
		HTTP:     &http.Client{Timeout: 10 * time.Minute},
		cutouts:  make(chan struct{}, parallel),
		visits:   make(chan struct{}, 2),
		catalogs: make(chan struct{}, 2),
	}
}

var fileRE = regexp.MustCompile(`level2_(\w+?_\d+_\d+)D(\d)_spx`)

// Visits lists the spectral images whose footprint contains the position,
// oldest first. The TAP tables see new data as soon as it is ingested, unlike
// SIA, at the cost of slower queries (tens of seconds).
func (c *Client) Visits(ctx context.Context, ra, dec float64) ([]Visit, error) {
	query := fmt.Sprintf(`SELECT a.uri, p.time_bounds_lower, p.energy_bandpassname
FROM spherex.artifact a
JOIN spherex.plane p ON a.planeid = p.planeid
WHERE 1 = CONTAINS(POINT('ICRS', %.6f, %.6f), p.poly)
ORDER BY p.time_bounds_lower`, ra, dec)
	q := url.Values{"LANG": {"ADQL"}, "FORMAT": {"csv"}, "QUERY": {query}}
	body, err := c.get(ctx, c.visits, c.Base+"/TAP/sync?"+q.Encode(), queryLimit)
	if err != nil {
		return nil, fmt.Errorf("irsa: TAP query: %w", err)
	}
	return parseVisits(body)
}

func parseVisits(body []byte) ([]Visit, error) {
	rows, err := csv.NewReader(strings.NewReader(string(body))).ReadAll()
	if err != nil {
		return nil, fmt.Errorf("irsa: TAP response: %w", err)
	}
	if len(rows) == 0 || len(rows[0]) < 2 || rows[0][0] != "uri" {
		return nil, fmt.Errorf("irsa: unexpected TAP response: %.200s", body)
	}
	return visitsFromRows(rows[1:]), nil
}

// visitsFromRows turns (archive path or URL, MJD) pairs into visits.
// Reprocessed files can appear next to older versions of the same exposure;
// keep one per exposure and detector, preferring data releases over quick releases.
func visitsFromRows(rows [][]string) []Visit {
	best := map[string]Visit{}
	for _, row := range rows {
		m := fileRE.FindStringSubmatch(row[0])
		mjd, err := strconv.ParseFloat(row[1], 64)
		if m == nil || err != nil {
			continue
		}
		path := strings.TrimPrefix(row[0], "/")
		if i := strings.Index(path, "ibe/data/spherex/"); i > 0 {
			path = path[i:]
		}
		release := ""
		if parts := strings.Split(strings.TrimPrefix(path, "ibe/data/spherex/"), "/"); len(parts) > 1 {
			release = parts[0]
		}
		det, _ := strconv.Atoi(m[2])
		v := Visit{Path: path, MJD: mjd, Detector: det, Exposure: m[1], Release: release}
		key := v.Exposure + "/" + m[2]
		if old, ok := best[key]; !ok || rank(v) > rank(old) {
			best[key] = v
		}
	}
	out := make([]Visit, 0, len(best))
	for _, v := range best {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].MJD != out[j].MJD {
			return out[i].MJD < out[j].MJD
		}
		return out[i].Detector < out[j].Detector
	})
	return out
}

func rank(v Visit) string {
	r := "0"
	if strings.HasPrefix(v.Release, "dr") {
		r = "1"
	}
	return r + v.Path
}

// ValidPath accepts only SPHEREx archive files, so the proxy cannot be used to
// fetch anything else.
func ValidPath(p string) bool {
	return strings.HasPrefix(p, "ibe/data/spherex/") && strings.HasSuffix(p, ".fits") &&
		!strings.Contains(p, "..") && !strings.ContainsAny(p, "?#\\")
}

// Cutout downloads a cutout of size degrees around (ra, dec) and drops the EPSF
// extension, a PSF cube that makes up about 95% of every cutout.
func (c *Client) Cutout(ctx context.Context, path string, ra, dec, size float64) ([]byte, error) {
	if !ValidPath(path) {
		return nil, errors.New("irsa: not a SPHEREx archive path")
	}
	u := fmt.Sprintf("%s/%s?center=%.6f,%.6fd&size=%.5f", c.Base, path, ra, dec, size)
	body, err := c.get(ctx, c.cutouts, u, cutoutLimit)
	if err != nil {
		return nil, fmt.Errorf("irsa: cutout: %w", err)
	}
	hdus, err := fits.Parse(body)
	if err != nil {
		return nil, fmt.Errorf("irsa: cutout: %w", err)
	}
	kept := hdus[:0]
	for _, h := range hdus {
		if h.Name() != "EPSF" {
			kept = append(kept, h)
		}
	}
	return fits.Join(kept), nil
}

// Time limits for one request to IRSA once it has a slot; TAP queries on big
// catalogs take minutes.
const (
	cutoutLimit = 90 * time.Second
	queryLimit  = 8 * time.Minute
)

type statusError int

func (e statusError) Error() string { return fmt.Sprintf("HTTP %d", int(e)) }

// get retries transient failures, which the archive returns now and then for
// cutouts under load.
//
// Waiting for a slot ends with ctx, so abandoned requests leave the queue;
// once a request to IRSA starts it runs to the end (within limit), so its
// answer still reaches the cache.
func (c *Client) get(ctx context.Context, slots chan struct{}, u string, limit time.Duration) ([]byte, error) {
	select {
	case slots <- struct{}{}:
		defer func() { <-slots }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), limit)
	defer cancel()
	var err error
	for attempt := range 3 {
		if attempt > 0 {
			select {
			case <-time.After(time.Duration(attempt) * time.Second):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		var body []byte
		body, err = c.once(ctx, u)
		var se statusError
		if err == nil || ctx.Err() != nil || (errors.As(err, &se) && se < 500) {
			return body, err
		}
	}
	return nil, err
}

func (c *Client) once(ctx context.Context, u string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "spherex-sky-timeline/0.1 (+Space Apps 2026)")
	res, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, statusError(res.StatusCode)
	}
	return io.ReadAll(res.Body)
}
