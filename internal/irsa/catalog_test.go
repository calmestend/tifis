package irsa

import (
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"spherex/internal/sky"
)

func TestCatWISEAndMerge(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Query().Get("QUERY"), "catwise_2020") {
			http.Error(w, "unexpected query", http.StatusBadRequest)
			return
		}
		// Barnard's Star (also in Gaia) and WISE 0855 split in two entries.
		w.Write([]byte("source_name,ra,dec,pmra,pmdec,w2mpro\n" +
			"J175747.66+044419.7,269.4485837,4.7376900,-0.80,10.36,4.1\n" +
			"J085507.96-071439.3,133.7831722,-7.2442516,-8.02120,1.03570,15.787\n" +
			"J085508.34-071439.2,133.7847613,-7.2442498,-7.96500,0.80620,15.721\n"))
	}))
	defer srv.Close()

	cw, err := NewClient(srv.URL, 2).CatWISE(context.Background(), 269.45, 4.77, 0.15)
	if err != nil {
		t.Fatal(err)
	}
	if len(cw) != 3 || math.Abs(cw[2].PMRA+7965) > 0.5 || cw[0].MJD != catwiseEpochMJD {
		t.Fatalf("CatWISE entries %+v; proper motions should be in mas/yr", cw)
	}
	gaia := []sky.CatalogEntry{{Catalog: "Gaia DR3", Name: "Gaia DR3 4472832130942575872",
		RA: 269.44850252544, Dec: 4.73942005111, PMRA: -801.551, PMDec: 10362.394, MJD: sky.GaiaEpoch, Mag: 8.19}}
	got := Merge(gaia, cw)
	if len(got) != 2 || got[0].Catalog != "Gaia DR3" || !strings.Contains(got[1].Name, "J085508.34") {
		t.Fatalf("merged %+v; want Gaia's Barnard plus the brighter WISE 0855 entry", got)
	}
}
