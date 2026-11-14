package irsa

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

const siaVOTable = `<?xml version="1.0" encoding="utf-8"?>
<VOTABLE version="1.3" xmlns="http://www.ivoa.net/xml/VOTable/v1.3">
 <RESOURCE type="meta"><TABLE><FIELD name="ignored"/></TABLE></RESOURCE>
 <RESOURCE type="results"><TABLE>
  <FIELD name="s_ra"/><FIELD name="access_url"/><FIELD name="t_min"/>
  <DATA><TABLEDATA>
   <TR><TD>270.1</TD><TD>https://irsa.ipac.caltech.edu/ibe/data/spherex/qr3/level2/2026W33_2A/l2b-v27-2026-241/3/level2_2026W33_2A_0594_4D3_spx_l2b-v27-2026-241.fits</TD><TD>61268.630812</TD></TR>
   <TR><TD>270.1</TD><TD>https://irsa.ipac.caltech.edu/ibe/data/spherex/dr1/level2/2025W33_2C/l2b-v27-2026-228/6/level2_2025W33_2C_0321_1D6_spx_l2b-v27-2026-228.fits</TD><TD>60902.864715</TD></TR>
  </TABLEDATA></DATA>
 </TABLE></RESOURCE>
</VOTABLE>`

func TestVisitsSIA(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if c := r.URL.Query()["COLLECTION"]; len(c) != 2 {
			t.Errorf("collections %v", c)
		}
		w.Write([]byte(siaVOTable))
	}))
	defer srv.Close()
	v, err := NewClient(srv.URL, 2).VisitsSIA(context.Background(), 269.45, 4.77)
	if err != nil {
		t.Fatal(err)
	}
	if len(v) != 2 || v[0].Detector != 6 || v[0].Exposure != "2025W33_2C_0321_1" || v[1].Release != "qr3" {
		t.Fatalf("got %+v", v)
	}
	if v[0].Path != "ibe/data/spherex/dr1/level2/2025W33_2C/l2b-v27-2026-228/6/level2_2025W33_2C_0321_1D6_spx_l2b-v27-2026-228.fits" {
		t.Errorf("path %q should be relative to the archive root", v[0].Path)
	}
}

// SPHEREX_SIA=/path/to/response.xml checks a saved IRSA SIA answer.
func TestRealSIAResponse(t *testing.T) {
	path := os.Getenv("SPHEREX_SIA")
	if path == "" {
		t.Skip("SPHEREX_SIA not set")
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := votableColumns(body, "access_url", "t_min")
	if err != nil {
		t.Fatal(err)
	}
	v := visitsFromRows(rows)
	per := map[int]int{}
	for _, x := range v {
		per[x.Detector]++
	}
	t.Logf("%d rows, %d visits, per detector %v", len(rows), len(v), per)
}
