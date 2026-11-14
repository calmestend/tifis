package fits

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"testing"
)

func header(cards ...string) []byte {
	var b strings.Builder
	for _, c := range cards {
		fmt.Fprintf(&b, "%-80s", c)
	}
	fmt.Fprintf(&b, "%-80s", "END")
	s := b.String()
	return []byte(s + strings.Repeat(" ", padded(len(s))-len(s)))
}

func image(name string, w, h int) HDU {
	return HDU{
		Header: header("XTENSION= 'IMAGE   '", "BITPIX  =                  -32", "NAXIS   =                    2",
			fmt.Sprintf("NAXIS1  = %20d", w), fmt.Sprintf("NAXIS2  = %20d", h),
			"PCOUNT  =                    0", "GCOUNT  =                    1",
			fmt.Sprintf("EXTNAME = '%-8s'           / extension name", name)),
		Data: bytes.Repeat([]byte{1}, padded(4*w*h)),
	}
}

func TestParseAndJoin(t *testing.T) {
	primary := HDU{Header: header("SIMPLE  =                    T", "BITPIX  =                    8", "NAXIS   =                    0", "VERSION = '7.0.5   '")}
	in := []HDU{primary, image("IMAGE", 58, 59), image("EPSF", 300, 400), image("ZODI", 58, 59)}
	hdus, err := Parse(Join(in))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, h := range hdus {
		names = append(names, h.Name())
	}
	if got := strings.Join(names, ","); got != "PRIMARY,IMAGE,EPSF,ZODI" {
		t.Fatalf("names %s", got)
	}
	if hdus[0].Keyword("VERSION") != "7.0.5" {
		t.Errorf("VERSION = %q", hdus[0].Keyword("VERSION"))
	}
	if len(hdus[2].Data) != padded(4*300*400) {
		t.Errorf("EPSF data size %d", len(hdus[2].Data))
	}
}

func TestTruncated(t *testing.T) {
	data := Join([]HDU{{Header: header("SIMPLE  =                    T", "BITPIX  =                    8", "NAXIS   =                    0")}, image("IMAGE", 58, 59)})
	if _, err := Parse(data[:len(data)-block]); err == nil {
		t.Fatal("expected an error for truncated data")
	}
}

// SPHEREX_CUTOUT=/path/to/cutout.fits checks a real IRSA cutout.
func TestRealCutout(t *testing.T) {
	path := os.Getenv("SPHEREX_CUTOUT")
	if path == "" {
		t.Skip("SPHEREX_CUTOUT not set")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	hdus, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range hdus {
		t.Logf("%-8s %8d bytes", h.Name(), len(h.Data))
	}
}
