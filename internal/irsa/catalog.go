package irsa

import (
	"context"
	"encoding/csv"
	"fmt"
	"math"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"spherex/internal/sky"
)

// CatWISE2020 fits positions at this epoch (2015.4) together with proper motions.
const catwiseEpochMJD = 57170.0

// CatWISE lists CatWISE2020 sources within radius degrees. CatWISE adds what
// Gaia cannot see, like cold brown dwarfs, with infrared proper motions.
func (c *Client) CatWISE(ctx context.Context, ra, dec, radius float64) ([]sky.CatalogEntry, error) {
	rows, err := c.tap(ctx, fmt.Sprintf("SELECT source_name, ra, dec, pmra, pmdec, w2mpro FROM catwise_2020 "+
		"WHERE 1 = CONTAINS(POINT('ICRS', ra, dec), CIRCLE('ICRS', %.6f, %.6f, %.5f)) "+
		"AND (w1mpro < 15.5 OR w2mpro < 16.5)", ra, dec, radius))
	if err != nil {
		return nil, fmt.Errorf("irsa: CatWISE: %w", err)
	}
	out := make([]sky.CatalogEntry, 0, len(rows))
	for _, r := range rows {
		out = append(out, sky.CatalogEntry{
			Catalog: "CatWISE2020", Name: "CatWISE " + r[0],
			RA: num(r[1]), Dec: num(r[2]), PMRA: 1000 * num(r[3]), PMDec: 1000 * num(r[4]),
			MJD: catwiseEpochMJD, Mag: num(r[5]),
		})
	}
	return out, nil
}

// Merge adds to the Gaia sources the CatWISE ones Gaia does not have.
// CatWISE lists some fast movers more than once; the brightest entry is kept.
func Merge(gaia, catwise []sky.CatalogEntry) []sky.CatalogEntry {
	out := append([]sky.CatalogEntry(nil), gaia...)
	cat := append([]sky.CatalogEntry(nil), catwise...)
	sort.Slice(cat, func(i, j int) bool { return cat[i].Mag < cat[j].Mag })
	for _, e := range cat {
		if !duplicate(e, out, len(gaia)) {
			out = append(out, e)
		}
	}
	return out
}

// duplicate reports whether e is within 3" of a listed source once both are
// moved to e's epoch.
func duplicate(e sky.CatalogEntry, list []sky.CatalogEntry, nGaia int) bool {
	for i, o := range list {
		s := sky.Source{RA: o.RA, Dec: o.Dec, PMRA: o.PMRA, PMDec: o.PMDec, RefMJD: o.MJD}
		ra, dec := s.Position(e.MJD)
		limit := 3.0
		if i >= nGaia {
			// Split CatWISE detections of one fast mover can sit further apart.
			limit = 6.0
		}
		if sky.Separation(ra, dec, e.RA, e.Dec)*3600 < limit {
			return true
		}
	}
	return false
}

func num(s string) float64 {
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || math.IsNaN(v) {
		return 0
	}
	return v
}

// tap runs an ADQL query and returns the CSV rows without the header.
func (c *Client) tap(ctx context.Context, query string) ([][]string, error) {
	q := url.Values{"LANG": {"ADQL"}, "FORMAT": {"csv"}, "QUERY": {query}}
	body, err := c.get(ctx, c.catalogs, c.Base+"/TAP/sync?"+q.Encode(), queryLimit)
	if err != nil {
		return nil, err
	}
	rows, err := csv.NewReader(strings.NewReader(string(body))).ReadAll()
	if err != nil || len(rows) == 0 {
		return nil, fmt.Errorf("unexpected TAP response: %.200s", body)
	}
	return rows[1:], nil
}
