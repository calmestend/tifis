package gaia

import "math"

// Gaia source_id values carry the HEALPix index (nested scheme, order 12) of
// the source in their top bits: source_id / 2^35. Bulk files are ordered by
// source_id, so by sky pixel.

// Order of the pixels used as cache tiles: about 0.23° across.
const Order = 8

// PixelOf returns the order-8 pixel encoded in a source_id.
func PixelOf(sourceID int64) int64 {
	return sourceID >> (35 + 2*(12-Order))
}

// Ang2Pix returns the nested HEALPix pixel containing (ra, dec) in degrees.
func Ang2Pix(order int, ra, dec float64) int64 {
	nside := int64(1) << order
	z := math.Sin(dec * math.Pi / 180)
	za := math.Abs(z)
	tt := math.Mod(ra/90, 4) // phi in units of pi/2
	if tt < 0 {
		tt += 4
	}
	var face, ix, iy int64
	if za <= 2.0/3 {
		// Equatorial region: faces 0-11 along the two families of edge lines.
		t1 := float64(nside) * (0.5 + tt)
		t2 := float64(nside) * z * 0.75
		jp := int64(t1 - t2)
		jm := int64(t1 + t2)
		ifp, ifm := jp/nside, jm/nside
		switch {
		case ifp == ifm:
			face = ifp | 4
		case ifp < ifm:
			face = ifp
		default:
			face = ifm + 8
		}
		ix = jm & (nside - 1)
		iy = nside - (jp & (nside - 1)) - 1
	} else {
		// Polar caps.
		ntt := min(int64(tt), 3)
		tp := tt - float64(ntt)
		tmp := float64(nside) * math.Sqrt(3*(1-za))
		jp := min(int64(tp*tmp), nside-1)
		jm := min(int64((1-tp)*tmp), nside-1)
		if z >= 0 {
			face, ix, iy = ntt, nside-jm-1, nside-jp-1
		} else {
			face, ix, iy = ntt+8, jp, jm
		}
	}
	return face<<(2*order) + spread(ix) + spread(iy)<<1
}

// spread interleaves the bits of v with zeros: abc -> 0a0b0c.
func spread(v int64) int64 {
	var out int64
	for i := 0; v != 0; i++ {
		out |= (v & 1) << (2 * i)
		v >>= 1
	}
	return out
}

// DiscPixels lists the order-8 pixels touching a circle of radius degrees,
// by sampling it more finely than the pixel size.
func DiscPixels(ra, dec, radius float64) []int64 {
	step := 0.05 // degrees, a quarter of a pixel
	seen := map[int64]bool{Ang2Pix(Order, ra, dec): true}
	cosDec := math.Max(math.Cos(dec*math.Pi/180), 1e-6)
	n := int(math.Ceil(radius / step))
	for j := -n; j <= n; j++ {
		for i := -n; i <= n; i++ {
			dy, dx := float64(j)*step, float64(i)*step
			if math.Hypot(dx, dy) > radius+step {
				continue
			}
			d := math.Max(-90, math.Min(90, dec+dy))
			seen[Ang2Pix(Order, ra+dx/cosDec, d)] = true
		}
	}
	out := make([]int64, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	return out
}
