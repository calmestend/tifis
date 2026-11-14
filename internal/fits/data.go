package fits

import (
	"encoding/binary"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Float returns a numeric keyword, or def when it is missing or not a number.
func (h HDU) Float(name string, def float64) float64 {
	v, err := strconv.ParseFloat(strings.ReplaceAll(h.Keyword(name), "D", "E"), 64)
	if err != nil {
		return def
	}
	return v
}

func (h HDU) Int(name string, def int) int {
	v, err := strconv.Atoi(h.Keyword(name))
	if err != nil {
		return def
	}
	return v
}

// Keywords returns every keyword starting with prefix, in header order.
func (h HDU) Keywords(prefix string) map[string]string {
	out := map[string]string{}
	for i := 0; i+80 <= len(h.Header); i += 80 {
		name := strings.TrimSpace(string(h.Header[i : i+8]))
		if strings.HasPrefix(name, prefix) && string(h.Header[i+8:i+10]) == "= " {
			out[name] = h.Keyword(name)
		}
	}
	return out
}

// Image decodes a 2D image as float32, row-major with the first FITS row first.
// BITPIX 32 integer images are converted too, so FLAGS can be read the same way.
func (h HDU) Image() (data []float32, w, hgt int, err error) {
	w, hgt = h.Int("NAXIS1", 0), h.Int("NAXIS2", 0)
	n := w * hgt
	bitpix := h.Int("BITPIX", 0)
	if h.Int("NAXIS", 0) != 2 || n == 0 || len(h.Data) < n*max(bitpix, -bitpix)/8 {
		return nil, 0, 0, fmt.Errorf("fits: %s is not a 2D image", h.Name())
	}
	data = make([]float32, n)
	switch bitpix {
	case -32:
		for i := range data {
			data[i] = math.Float32frombits(binary.BigEndian.Uint32(h.Data[4*i:]))
		}
	case 32:
		for i := range data {
			data[i] = float32(int32(binary.BigEndian.Uint32(h.Data[4*i:])))
		}
	default:
		return nil, 0, 0, fmt.Errorf("fits: unsupported BITPIX %d in %s", bitpix, h.Name())
	}
	return data, w, hgt, nil
}

// Uint32Image decodes a BITPIX 32 image bit for bit, as needed for flag masks.
func (h HDU) Uint32Image() (data []uint32, w, hgt int, err error) {
	w, hgt = h.Int("NAXIS1", 0), h.Int("NAXIS2", 0)
	n := w * hgt
	if h.Int("BITPIX", 0) != 32 || h.Int("NAXIS", 0) != 2 || n == 0 || len(h.Data) < 4*n {
		return nil, 0, 0, fmt.Errorf("fits: %s is not a 32-bit 2D image", h.Name())
	}
	data = make([]uint32, n)
	for i := range data {
		data[i] = binary.BigEndian.Uint32(h.Data[4*i:])
	}
	return data, w, hgt, nil
}

// Column reads one column of the first row of a binary table as float64.
// Only the J (int32) and E (float32) formats used by SPHEREx tables are handled.
func (h HDU) Column(name string) ([]float64, error) {
	if h.Keyword("XTENSION") != "BINTABLE" {
		return nil, fmt.Errorf("fits: %s is not a binary table", h.Name())
	}
	off := 0
	for i := 1; i <= h.Int("TFIELDS", 0); i++ {
		form := h.Keyword(fmt.Sprintf("TFORM%d", i))
		count, kind := 1, form
		if j := strings.IndexAny(form, "JEDIBL"); j > 0 {
			count, _ = strconv.Atoi(form[:j])
			kind = form[j:]
		}
		size := map[string]int{"J": 4, "E": 4, "D": 8, "I": 2, "B": 1, "L": 1}[kind[:1]]
		if !strings.EqualFold(h.Keyword(fmt.Sprintf("TTYPE%d", i)), name) {
			off += count * size
			continue
		}
		if off+count*size > len(h.Data) {
			return nil, fmt.Errorf("fits: column %s out of range", name)
		}
		out := make([]float64, count)
		for k := range out {
			b := h.Data[off+k*size:]
			switch kind[:1] {
			case "J":
				out[k] = float64(int32(binary.BigEndian.Uint32(b)))
			case "E":
				out[k] = float64(math.Float32frombits(binary.BigEndian.Uint32(b)))
			case "D":
				out[k] = math.Float64frombits(binary.BigEndian.Uint64(b))
			default:
				return nil, fmt.Errorf("fits: unsupported column format %s", form)
			}
		}
		return out, nil
	}
	return nil, fmt.Errorf("fits: no column %s in %s", name, h.Name())
}

// Find returns the HDU with the given EXTNAME.
func Find(hdus []HDU, name string) (HDU, bool) {
	for _, h := range hdus {
		if h.Name() == name {
			return h, true
		}
	}
	return HDU{}, false
}
