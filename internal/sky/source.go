package sky

import "math"

type SED int

const (
	SEDStar SED = iota
	SEDBrownDwarf
	SEDYDwarf
	SEDMira
	SEDYoung
)

// Source is a point source with linear proper motion and an optional light curve.
type Source struct {
	Name        string
	RA, Dec     float64 // degrees at RefMJD
	PMRA, PMDec float64 // mas/yr, PMRA includes cos(dec)
	RefMJD      float64
	Kind        SED
	Temp        float64 // K
	Flux        float64 // F_nu at RefLambda, image units summed over the PSF
	RefLambda   float64 // µm
	Period      float64 // days
	Amp         float64 // mag at 1.2 µm
	Phase       float64 // MJD of maximum
	Catalog     bool    // listed in the Gaia/WISE layer
	CatalogName string  // which catalog the position comes from
	CatalogMJD  float64 // epoch of the catalog position; 0 means Gaia DR3 (J2016.0)
	CatalogMag  float64 // Gaia G or WISE W1/W2 from the catalog; 0 when unknown
	seed        uint64
}

// GaiaEpoch is J2016.0, the reference epoch of Gaia DR3 positions.
const GaiaEpoch = 57388.5

func (s *Source) CatalogEpoch() float64 {
	if s.CatalogMJD == 0 {
		return GaiaEpoch
	}
	return s.CatalogMJD
}

func (s *Source) Position(mjd float64) (ra, dec float64) {
	yr := (mjd - s.RefMJD) / 365.25
	dec = s.Dec + s.PMDec*yr/3.6e6
	ra = s.RA + s.PMRA*yr/3.6e6/math.Cos(s.Dec*deg)
	return ra, dec
}

func (s *Source) FluxAt(lambda, mjd float64) float64 {
	ref := s.shape(s.RefLambda)
	if ref <= 0 {
		return 0
	}
	return s.Flux * s.shape(lambda) / ref * math.Pow(10, -0.4*s.dmag(lambda, mjd))
}

type band struct{ center, width, depth float64 }

var (
	brownDwarfBands = []band{
		{1.15, 0.05, 0.5}, {1.4, 0.08, 0.75}, {1.65, 0.04, 0.45}, {1.9, 0.10, 0.75},
		{2.25, 0.12, 0.5}, {2.7, 0.20, 0.8}, {3.3, 0.15, 0.85}, {4.6, 0.12, 0.35},
	}
	miraBands = []band{
		{1.4, 0.08, 0.4}, {1.9, 0.10, 0.4}, {2.3, 0.05, 0.3}, {2.7, 0.20, 0.5}, {4.6, 0.10, 0.35},
	}
	youngBands = []band{
		{3.05, 0.15, 0.25}, {4.67, 0.06, 0.2},
	}
)

// planckNu is B_nu up to a constant, lambda in µm.
func planckNu(lambda, temp float64) float64 {
	x := 14387.77 / (lambda * temp)
	if x > 700 {
		return 0
	}
	return 1 / (lambda * lambda * lambda * math.Expm1(x))
}

func absorb(f, lambda float64, bands []band) float64 {
	for _, b := range bands {
		d := (lambda - b.center) / b.width
		f *= 1 - b.depth*math.Exp(-0.5*d*d)
	}
	return f
}

func (s *Source) shape(lambda float64) float64 {
	switch s.Kind {
	case SEDBrownDwarf, SEDYDwarf:
		return absorb(planckNu(lambda, s.Temp), lambda, brownDwarfBands)
	case SEDMira:
		return absorb(planckNu(lambda, s.Temp), lambda, miraBands)
	case SEDYoung:
		star := planckNu(lambda, s.Temp) / planckNu(2.2, s.Temp)
		disk := 1.2 * planckNu(lambda, 1100) / planckNu(2.2, 1100)
		return absorb(star+disk, lambda, youngBands)
	default:
		return planckNu(lambda, s.Temp)
	}
}

// dmag is the magnitude offset at a given time; positive means fainter.
func (s *Source) dmag(lambda, mjd float64) float64 {
	switch s.Kind {
	case SEDMira:
		amp := s.Amp * math.Pow(1.2/lambda, 0.6)
		return -amp * math.Cos(2*math.Pi*(mjd-s.Phase)/s.Period)
	case SEDYoung:
		amp := s.Amp * math.Pow(1.2/lambda, 0.8)
		return amp * irregular(mjd, s.seed)
	}
	return 0
}

// irregular is a smooth, deterministic wobble in [-1, 1] with occasional dips,
// shaped like the dimming episodes of young stars.
func irregular(mjd float64, seed uint64) float64 {
	r := newRand(seed)
	v := 0.0
	for _, p := range []float64{3.1, 11.7, 37.0, 142.0} {
		v += 0.25 * math.Sin(2*math.Pi*mjd/p+r.Float64()*2*math.Pi)
	}
	dip := math.Sin(2*math.Pi*mjd/410 + r.Float64()*2*math.Pi)
	if dip > 0.6 {
		v += 2.5 * (dip - 0.6)
	}
	return math.Max(-1, math.Min(1.5, v))
}

func fluxFromMag(mag float64) float64 {
	return 10 * math.Pow(10, -0.4*(mag-10))
}
