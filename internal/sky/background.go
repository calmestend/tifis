package sky

import "math"

// SubtractPlane removes a tilted background a + b·x + c·y fitted to the
// unflagged pixels, ignoring sources with two passes of sigma clipping.
// Zodiacal light changes between visits, so differences need this.
func SubtractPlane(img []float32, flags []Flag, n int, sigma float64) {
	keep := func(i int, a, b, c float64) bool {
		if flags != nil && flags[i].Bad() {
			return false
		}
		x, y := float64(i%n), float64(i/n)
		return math.Abs(float64(img[i])-(a+b*x+c*y)) < 3*sigma
	}
	median, _ := Stats(img, flags)
	a, b, c := median, 0.0, 0.0
	for range 2 {
		// Normal equations for least squares in (1, x, y).
		var s [3][3]float64
		var r [3]float64
		for i, v := range img {
			if !keep(i, a, b, c) {
				continue
			}
			p := [3]float64{1, float64(i % n), float64(i / n)}
			for j := range 3 {
				r[j] += p[j] * float64(v)
				for k := range 3 {
					s[j][k] += p[j] * p[k]
				}
			}
		}
		sol, ok := solve3(s, r)
		if !ok {
			break
		}
		a, b, c = sol[0], sol[1], sol[2]
	}
	for i := range img {
		img[i] -= float32(a + b*float64(i%n) + c*float64(i/n))
	}
}

func solve3(m [3][3]float64, r [3]float64) ([3]float64, bool) {
	det := func(m [3][3]float64) float64 {
		return m[0][0]*(m[1][1]*m[2][2]-m[1][2]*m[2][1]) -
			m[0][1]*(m[1][0]*m[2][2]-m[1][2]*m[2][0]) +
			m[0][2]*(m[1][0]*m[2][1]-m[1][1]*m[2][0])
	}
	d := det(m)
	if math.Abs(d) < 1e-12 {
		return [3]float64{}, false
	}
	var out [3]float64
	for col := range 3 {
		mc := m
		for row := range 3 {
			mc[row][col] = r[row]
		}
		out[col] = det(mc) / d
	}
	return out, true
}
