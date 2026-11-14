package irsa

import (
	"context"
	"encoding/xml"
	"fmt"
	"net/url"
)

// VisitsSIA lists the same spectral images as Visits through Simple Image
// Access, which answers in seconds rather than tens of seconds but lags new
// data by about a day (see the IRSA SPHEREx intro tutorial).
func (c *Client) VisitsSIA(ctx context.Context, ra, dec float64) ([]Visit, error) {
	q := url.Values{
		"COLLECTION":     {"spherex_dr1", "spherex_qr3"},
		"POS":            {fmt.Sprintf("circle %.6f %.6f 0.0003", ra, dec)},
		"RESPONSEFORMAT": {"VOTABLE"},
	}
	body, err := c.get(ctx, c.visits, c.Base+"/SIA?"+q.Encode(), queryLimit)
	if err != nil {
		return nil, fmt.Errorf("irsa: SIA query: %w", err)
	}
	rows, err := votableColumns(body, "access_url", "t_min")
	if err != nil {
		return nil, fmt.Errorf("irsa: SIA response: %w", err)
	}
	return visitsFromRows(rows), nil
}

// votableColumns reads the named columns of the last table in a TABLEDATA VOTable.
func votableColumns(body []byte, names ...string) ([][]string, error) {
	var doc struct {
		Tables []struct {
			Fields []struct {
				Name string `xml:"name,attr"`
			} `xml:"FIELD"`
			Rows []struct {
				Cells []string `xml:"TD"`
			} `xml:"DATA>TABLEDATA>TR"`
		} `xml:"RESOURCE>TABLE"`
	}
	if err := xml.Unmarshal(body, &doc); err != nil {
		return nil, err
	}
	if len(doc.Tables) == 0 {
		return nil, fmt.Errorf("no table in %.200s", body)
	}
	t := doc.Tables[len(doc.Tables)-1]
	idx := make([]int, len(names))
	for k, name := range names {
		idx[k] = -1
		for i, f := range t.Fields {
			if f.Name == name {
				idx[k] = i
			}
		}
		if idx[k] < 0 {
			return nil, fmt.Errorf("no column %s", name)
		}
	}
	out := make([][]string, 0, len(t.Rows))
	for _, r := range t.Rows {
		row := make([]string, len(names))
		for k, i := range idx {
			if i < len(r.Cells) {
				row[k] = r.Cells[i]
			}
		}
		out = append(out, row)
	}
	return out, nil
}
