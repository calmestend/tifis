// Package fits splits and joins FITS files at the HDU level. It reads headers
// but leaves data untouched, which is all the proxy needs to drop extensions.
package fits

import (
	"bytes"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

const block = 2880

type HDU struct {
	Header []byte // whole header blocks, including END and padding
	Data   []byte // data blocks, including padding
}

// Keyword returns the value of a header keyword with quotes and comments removed.
func (h HDU) Keyword(name string) string {
	for i := 0; i+80 <= len(h.Header); i += 80 {
		card := string(h.Header[i : i+80])
		if strings.TrimSpace(card[:8]) != name || card[8:10] != "= " {
			continue
		}
		v := card[10:]
		if strings.HasPrefix(strings.TrimSpace(v), "'") {
			v = strings.TrimSpace(v)
			if end := strings.Index(v[1:], "'"); end >= 0 {
				return strings.TrimSpace(v[1 : end+1])
			}
		}
		if c := strings.Index(v, "/"); c >= 0 {
			v = v[:c]
		}
		return strings.TrimSpace(v)
	}
	return ""
}

func (h HDU) Name() string {
	if n := h.Keyword("EXTNAME"); n != "" {
		return n
	}
	return "PRIMARY"
}

func (h HDU) dataSize() (int, error) {
	naxis, err := strconv.Atoi(h.Keyword("NAXIS"))
	if err != nil {
		return 0, fmt.Errorf("fits: bad NAXIS in %s", h.Name())
	}
	if naxis == 0 {
		return 0, nil
	}
	bitpix, err := strconv.Atoi(h.Keyword("BITPIX"))
	if err != nil {
		return 0, fmt.Errorf("fits: bad BITPIX in %s", h.Name())
	}
	size := max(bitpix, -bitpix) / 8
	for i := 1; i <= naxis; i++ {
		n, err := strconv.Atoi(h.Keyword(fmt.Sprintf("NAXIS%d", i)))
		if err != nil {
			return 0, fmt.Errorf("fits: bad NAXIS%d in %s", i, h.Name())
		}
		size *= n
	}
	gcount := 1
	if g := h.Keyword("GCOUNT"); g != "" {
		gcount, _ = strconv.Atoi(g)
	}
	pcount := 0
	if p := h.Keyword("PCOUNT"); p != "" {
		pcount, _ = strconv.Atoi(p)
	}
	return gcount * (size + pcount), nil
}

func padded(n int) int {
	return (n + block - 1) / block * block
}

var endCard = []byte("END" + strings.Repeat(" ", 77))

// Parse splits a FITS file into its HDUs.
func Parse(data []byte) ([]HDU, error) {
	if len(data) < block || !bytes.HasPrefix(data, []byte("SIMPLE  =")) {
		return nil, errors.New("fits: not a FITS file")
	}
	var hdus []HDU
	off := 0
	for off < len(data) {
		start := off
		end := -1
		for end < 0 {
			if off+block > len(data) {
				return nil, errors.New("fits: truncated header")
			}
			for i := off; i < off+block; i += 80 {
				if bytes.Equal(data[i:i+80], endCard) {
					end = i
					break
				}
			}
			off += block
		}
		h := HDU{Header: data[start:off]}
		size, err := h.dataSize()
		if err != nil {
			return nil, err
		}
		if off+padded(size) > len(data) {
			return nil, fmt.Errorf("fits: truncated data in %s", h.Name())
		}
		h.Data = data[off : off+padded(size)]
		off += padded(size)
		hdus = append(hdus, h)
	}
	return hdus, nil
}

// Join writes HDUs back into a FITS file.
func Join(hdus []HDU) []byte {
	var b bytes.Buffer
	for _, h := range hdus {
		b.Write(h.Header)
		b.Write(h.Data)
	}
	return b.Bytes()
}
