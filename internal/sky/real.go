package sky

import (
	"errors"
	"fmt"
	"math"
	"sort"

	"spherex/internal/fits"
	"spherex/internal/wcs"
)

// ArchiveImage is one spectral image covering the field, as listed by the
// archive (see internal/irsa).
type ArchiveImage struct {
	Path     string  `json:"path"`
	MJD      float64 `json:"mjd"`
	Detector int     `json:"detector"`
	Exposure string  `json:"exposure"`
}

// seasonGap separates survey passes: within a pass a position is revisited
// over days to weeks, and passes are about six months apart.
const seasonGap = 40

// NewRealField plans a field from archive images. Each exposure covers the
// position with one pair of detectors (D1+D4, D2+D5 or D3+D6), so a visit has
// frames in two channels and the other four are unavailable.
func NewRealField(ra, dec, fovArcmin float64, images []ArchiveImage, nowMJD float64) *Field {
	n := max(16, int(math.Round(fovArcmin*60/PixScale)))
	f := &Field{
		WCS:    WCS{RA: ra, Dec: dec, Scale: PixScale, CX: float64(n) / 2, CY: float64(n) / 2},
		N:      n,
		FOV:    fovArcmin,
		Target: -1,
		Real:   true,
		// Pixel values are MJy/sr; an aperture sum times the pixel solid angle
		// in sr is MJy, and 1e9 more gives mJy, the unit the archive tools use.
		FluxScale: math.Pow(PixScale*arcsec, 2) * 1e9,
		FluxUnit:  "mJy",
	}
	_, f.EclLat = Ecliptic(ra, dec)

	type exposure struct {
		mjd    float64
		images map[int]ArchiveImage
	}
	byID := map[string]*exposure{}
	for _, im := range images {
		if im.Detector < 1 || im.Detector > NumChannels {
			continue
		}
		e, ok := byID[im.Exposure]
		if !ok {
			e = &exposure{mjd: im.MJD, images: map[int]ArchiveImage{}}
			byID[im.Exposure] = e
		}
		e.mjd = math.Min(e.mjd, im.MJD)
		e.images[im.Detector-1] = im
	}
	exposures := make([]*exposure, 0, len(byID))
	for _, e := range byID {
		exposures = append(exposures, e)
	}
	sort.Slice(exposures, func(i, j int) bool { return exposures[i].mjd < exposures[j].mjd })

	season := 0
	for v, e := range exposures {
		if v > 0 && e.mjd-exposures[v-1].mjd > seasonGap {
			season++
		}
		// The sub-band is only known once a frame arrives and its spectral WCS is read.
		f.Visits = append(f.Visits, Visit{MJD: e.mjd, Season: season, SubBand: -1})
	}
	for c := range NumChannels {
		f.Frames[c] = make([]*Frame, len(exposures))
		for v, e := range exposures {
			im, ok := e.images[c]
			f.Frames[c][v] = &Frame{Channel: c, Visit: v, MJD: e.mjd, Available: ok, File: im.Path}
		}
	}

	for _, t := range Targets {
		tra, tdec := t.Source.Position(nowMJD)
		if Separation(ra, dec, tra, tdec) < fovArcmin/60 {
			f.TargetID, f.Name = t.ID, t.Name
			break
		}
	}
	return f
}

// LoadCutout decodes a cutout (as served by the proxy, without EPSF) and
// resamples it onto the field grid. Cutouts come in detector orientation with
// SIP distortion, so every field pixel is looked up through the image WCS.
// The ZODI model is subtracted so backgrounds match between visits.
func (f *Field) LoadCutout(fr *Frame, data []byte) error {
	hdus, err := fits.Parse(data)
	if err != nil {
		return err
	}
	get := func(name string) (fits.HDU, error) {
		h, ok := fits.Find(hdus, name)
		if !ok {
			return h, fmt.Errorf("cutout has no %s extension", name)
		}
		return h, nil
	}
	imgHDU, err := get("IMAGE")
	if err != nil {
		return err
	}
	cel, err := wcs.NewCelestial(imgHDU)
	if err != nil {
		return err
	}
	wave, err := wcs.NewWave(imgHDU, hdus)
	if err != nil {
		return err
	}
	img, w, h, err := imgHDU.Image()
	if err != nil {
		return err
	}
	var flags []uint32
	if fh, err := get("FLAGS"); err == nil {
		flags, _, _, _ = fh.Uint32Image()
	}
	var variance, zodi []float32
	if vh, err := get("VARIANCE"); err == nil {
		variance, _, _, _ = vh.Image()
	}
	if zh, err := get("ZODI"); err == nil {
		zodi, _, _, _ = zh.Image()
	}
	if len(flags) != w*h || len(variance) != w*h || len(zodi) != w*h {
		return errors.New("cutout layers have different sizes")
	}

	n := f.N
	out := make([]float32, n*n)
	outVar := make([]float32, n*n)
	outFlags := make([]Flag, n*n)
	for j := range n {
		for i := range n {
			p := j*n + i
			ra, dec := f.WCS.ToSky(float64(i)+0.5, float64(j)+0.5)
			x, y, ok := cel.SkyToPixel(ra, dec)
			xi, yi := int(math.Round(x)), int(math.Round(y))
			if !ok || xi < 0 || yi < 0 || xi >= w || yi >= h {
				outFlags[p] = FlagMissing
				continue
			}
			outFlags[p] = Flag(flags[yi*w+xi])
			v, ok := bilinear(img, zodi, w, h, x, y)
			if !ok {
				outFlags[p] |= FlagMissing
				continue
			}
			out[p] = v
			if vv, ok := bilinear(variance, nil, w, h, x, y); ok {
				outVar[p] = vv
			}
		}
	}

	fr.Image, fr.Variance, fr.Flags = out, outVar, outFlags
	fr.MJD = imgHDU.Float("MJD-AVG", fr.MJD)
	fr.waveAt = func(ra, dec float64) float64 {
		x, y, _ := cel.SkyToPixel(ra, dec)
		l, _ := wave.At(x, y)
		return l
	}
	fr.Lambda = fr.LambdaAt(f.WCS.RA, f.WCS.Dec)
	fr.Background, fr.Sigma = Stats(out, outFlags)
	fr.Loaded = true

	// The pair of detectors in a visit sees the source through the same part of
	// their filters, so either frame tells which sub-band the visit is.
	if vis := &f.Visits[fr.Visit]; vis.SubBand < 0 {
		ch := Channels[fr.Channel]
		vis.SubBand = math.Max(0, math.Min(SubBands-1, (fr.Lambda-ch.Lo)/(ch.Hi-ch.Lo)*SubBands-0.5))
	}
	return nil
}

// bilinear samples a-b (b may be nil) at 0-based (x, y), skipping non-finite
// pixels and reweighting the rest.
func bilinear(a, b []float32, w, h int, x, y float64) (float32, bool) {
	x0, y0 := int(math.Floor(x)), int(math.Floor(y))
	fx, fy := x-float64(x0), y-float64(y0)
	var sum, wsum float64
	for dy := range 2 {
		for dx := range 2 {
			xi, yi := x0+dx, y0+dy
			if xi < 0 || yi < 0 || xi >= w || yi >= h {
				continue
			}
			v := float64(a[yi*w+xi])
			if b != nil {
				v -= float64(b[yi*w+xi])
			}
			if math.IsNaN(v) || math.IsInf(v, 0) {
				continue
			}
			wt := (1 - math.Abs(float64(dx)-fx)) * (1 - math.Abs(float64(dy)-fy))
			sum += wt * v
			wsum += wt
		}
	}
	if wsum < 0.25 {
		return 0, false
	}
	return float32(sum / wsum), true
}

// CatalogEntry is a source from Gaia DR3 or CatWISE2020.
type CatalogEntry struct {
	Catalog string  `json:"catalog"`
	Name    string  `json:"name"`
	RA      float64 `json:"ra"`
	Dec     float64 `json:"dec"`
	PMRA    float64 `json:"pmra"`  // mas/yr, includes cos(dec)
	PMDec   float64 `json:"pmdec"` // mas/yr
	MJD     float64 `json:"mjd"`   // epoch of the position
	Mag     float64 `json:"mag"`   // Gaia G or WISE W1
}

// SetCatalog replaces the catalog sources of a real field and finds the test
// object among them by its predicted position.
func (f *Field) SetCatalog(entries []CatalogEntry) {
	f.Sources = f.Sources[:0]
	f.Target = -1
	var target *Source
	for i := range Targets {
		if Targets[i].ID == f.TargetID {
			target = &Targets[i].Source
		}
	}
	best := 5.0 / 3600 // degrees
	for _, e := range entries {
		f.Sources = append(f.Sources, Source{
			Name: e.Name, RA: e.RA, Dec: e.Dec, PMRA: e.PMRA, PMDec: e.PMDec,
			RefMJD: e.MJD, Catalog: true, CatalogName: e.Catalog, CatalogMJD: e.MJD, CatalogMag: e.Mag,
		})
		if target == nil {
			continue
		}
		tra, tdec := target.Position(e.MJD)
		if d := Separation(e.RA, e.Dec, tra, tdec); d < best {
			best = d
			f.Target = len(f.Sources) - 1
		}
	}
	if f.Target >= 0 {
		f.Sources[f.Target].Name = f.Name
	}
}
