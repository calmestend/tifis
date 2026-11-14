// Package gaia serves Gaia DR3 sources around a position from the bulk files
// on the ESA CDN. Those are 3386 gzip CSV files of ~230 MB, ordered by
// source_id and so by HEALPix pixel. A file is streamed once, split into
// order-8 pixel tiles on disk as it goes, and a request is answered as soon as
// the pixels it needs have gone by; the rest of the file keeps filling the
// cache for neighboring fields.
package gaia

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/binary"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
)

const (
	DefaultBase = "https://cdn.gea.esac.esa.int/Gaia/gdr3/gaia_source/"
	// The CDN's file browser lists this S3-style bucket.
	DefaultList = "https://gaia.eu-1.cdn77-storage.com/?prefix=Gaia/gdr3/gaia_source/&delimiter=/"
	// Epoch of Gaia DR3 positions, J2016.0.
	EpochMJD = 57388.5
)

type Star struct {
	SourceID int64
	RA, Dec  float64
	Parallax float32 // mas; NaN when Gaia has no astrometric solution
	PMRA     float32 // mas/yr, includes cos(dec)
	PMDec    float32
	G        float32
}

const starSize = 40

type sourceFile struct {
	Name   string
	Lo, Hi int64 // order-8 pixel range, inclusive
	Size   int64
}

type Store struct {
	Dir     string
	Base    string
	ListURL string
	HTTP    *http.Client
	// Parallel range requests per file and their size.
	Parallel int
	Chunk    int64

	mu      sync.Mutex
	files   []sourceFile
	streams map[string]*stream
	slots   chan struct{}
}

type stream struct {
	mu       sync.Mutex
	cond     *sync.Cond
	done     int64 // pixels below this are on disk
	finished bool
	err      error
	file     sourceFile
	reader   atomic.Pointer[rangeReader]
}

// Progress describes a bulk file being streamed.
type Progress struct {
	File     string  `json:"file"`
	Fraction float64 `json:"fraction"` // of the compressed file read so far
	MB       float64 `json:"mb"`       // compressed size
}

// Progress lists the files being streamed right now for a cone.
func (s *Store) Progress(ra, dec, radius float64) []Progress {
	pixels := DiscPixels(ra, dec, radius)
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []Progress{}
	for _, st := range s.streams {
		covers := false
		for _, p := range pixels {
			covers = covers || (p >= st.file.Lo && p <= st.file.Hi)
		}
		if !covers {
			continue
		}
		p := Progress{File: st.file.Name, MB: float64(st.file.Size) / 1e6}
		if rr := st.reader.Load(); rr != nil && st.file.Size > 0 {
			p.Fraction = float64(rr.read.Load()) / float64(st.file.Size)
		}
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].File < out[j].File })
	return out
}

func NewStore(dir string) (*Store, error) {
	if err := os.MkdirAll(filepath.Join(dir, "tiles"), 0o755); err != nil {
		return nil, err
	}
	return &Store{
		Dir: dir, Base: DefaultBase, ListURL: DefaultList,
		HTTP:     &http.Client{},
		Parallel: 8, Chunk: 4 << 20,
		streams: map[string]*stream{},
		// Each stream takes several connections and a CPU decompressing.
		slots: make(chan struct{}, 2),
	}, nil
}

// Cone returns the sources whose Gaia position is within radius degrees of
// (ra, dec). It blocks until the tiles it needs are on disk.
func (s *Store) Cone(ctx context.Context, ra, dec, radius float64) ([]Star, error) {
	files, err := s.index(ctx)
	if err != nil {
		return nil, err
	}
	var out []Star
	for _, pix := range DiscPixels(ra, dec, radius) {
		if err := s.ensure(ctx, files, pix); err != nil {
			return nil, err
		}
		stars, err := s.readTile(pix)
		if err != nil {
			return nil, err
		}
		for _, st := range stars {
			if separation(ra, dec, st.RA, st.Dec) <= radius {
				out = append(out, st)
			}
		}
	}
	return out, nil
}

// ensure waits until pixel pix is on disk, streaming its file if needed.
func (s *Store) ensure(ctx context.Context, files []sourceFile, pix int64) error {
	i := sort.Search(len(files), func(i int) bool { return files[i].Hi >= pix })
	if i == len(files) || files[i].Lo > pix {
		return fmt.Errorf("gaia: no bulk file covers pixel %d", pix)
	}
	f := files[i]
	if s.haveTile(pix) || s.fileDone(f.Name) {
		return nil
	}
	st := s.stream(f)
	stop := context.AfterFunc(ctx, func() {
		st.mu.Lock()
		st.cond.Broadcast()
		st.mu.Unlock()
	})
	defer stop()
	st.mu.Lock()
	defer st.mu.Unlock()
	for st.done <= pix && !st.finished && ctx.Err() == nil {
		st.cond.Wait()
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if st.done <= pix && st.err != nil {
		return st.err
	}
	return nil
}

func (s *Store) stream(f sourceFile) *stream {
	s.mu.Lock()
	defer s.mu.Unlock()
	if st, ok := s.streams[f.Name]; ok {
		return st
	}
	st := &stream{done: -1, file: f}
	st.cond = sync.NewCond(&st.mu)
	s.streams[f.Name] = st
	go func() {
		s.slots <- struct{}{}
		err := s.run(f, st)
		<-s.slots
		st.mu.Lock()
		st.err, st.finished = err, true
		if err == nil {
			st.done = f.Hi + 1
		}
		st.cond.Broadcast()
		st.mu.Unlock()
		s.mu.Lock()
		delete(s.streams, f.Name)
		s.mu.Unlock()
	}()
	return st
}

// run streams one bulk file into tiles. It is not tied to any request: once
// started it finishes, so the whole file ends up cached.
func (s *Store) run(f sourceFile, st *stream) error {
	ctx := context.Background()
	rr := newRangeReader(ctx, s.Base+f.Name, f.Size, s.Chunk, s.Parallel)
	st.reader.Store(rr)
	gz, err := gzip.NewReader(bufio.NewReaderSize(rr, 1<<20))
	if err != nil {
		return fmt.Errorf("gaia: %s: %w", f.Name, err)
	}
	defer gz.Close()
	lines := bufio.NewReaderSize(gz, 1<<20)

	var cols []int // indexes of source_id, ra, dec, parallax, pmra, pmdec, phot_g_mean_mag
	cur := int64(-1)
	var tile []Star
	flush := func(next int64) error {
		if cur >= 0 {
			if err := s.writeTile(cur, tile); err != nil {
				return err
			}
		}
		tile, cur = tile[:0], next
		st.mu.Lock()
		st.done = next
		st.cond.Broadcast()
		st.mu.Unlock()
		return nil
	}
	for {
		line, err := lines.ReadSlice('\n')
		if err == bufio.ErrBufferFull {
			return fmt.Errorf("gaia: %s: line too long", f.Name)
		}
		if len(line) > 0 && line[0] != '#' {
			line = bytes.TrimRight(line, "\r\n")
			if cols == nil {
				if cols, err = columns(line); err != nil {
					return fmt.Errorf("gaia: %s: %w", f.Name, err)
				}
				continue
			}
			star, ok := parseRow(line, cols)
			if ok {
				if pix := PixelOf(star.SourceID); pix != cur {
					if err := flush(pix); err != nil {
						return err
					}
				}
				tile = append(tile, star)
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("gaia: %s: %w", f.Name, err)
		}
	}
	if err := flush(f.Hi + 1); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(s.Dir, "done-"+f.Name), nil, 0o644)
}

var wanted = []string{"source_id", "ra", "dec", "parallax", "pmra", "pmdec", "phot_g_mean_mag"}

func columns(header []byte) ([]int, error) {
	names := bytes.Split(header, []byte(","))
	cols := make([]int, len(wanted))
	for k, w := range wanted {
		cols[k] = -1
		for i, n := range names {
			if string(n) == w {
				cols[k] = i
			}
		}
		if cols[k] < 0 {
			return nil, fmt.Errorf("no %s column", w)
		}
	}
	return cols, nil
}

// parseRow extracts the wanted fields from a CSV line. Gaia bulk files have no
// commas inside fields, so a plain split is enough.
func parseRow(line []byte, cols []int) (Star, bool) {
	last := 0
	for _, c := range cols {
		last = max(last, c)
	}
	fields := make([][]byte, last+1)
	for i, start := 0, 0; i <= last; i++ {
		end := bytes.IndexByte(line[start:], ',')
		if end < 0 {
			if i != last {
				return Star{}, false
			}
			end = len(line) - start
		}
		fields[i] = line[start : start+end]
		start += end + 1
		if start > len(line) && i < last {
			return Star{}, false
		}
	}
	id, err := strconv.ParseInt(string(fields[cols[0]]), 10, 64)
	if err != nil {
		return Star{}, false
	}
	f := func(k int) float64 {
		v, err := strconv.ParseFloat(string(fields[cols[k]]), 64)
		if err != nil {
			return math.NaN() // "null"
		}
		return v
	}
	return Star{SourceID: id, RA: f(1), Dec: f(2),
		Parallax: float32(f(3)), PMRA: float32(f(4)), PMDec: float32(f(5)), G: float32(f(6))}, true
}

func (s *Store) tilePath(pix int64) string {
	return filepath.Join(s.Dir, "tiles", strconv.FormatInt(pix/1000, 10), strconv.FormatInt(pix, 10)+".bin")
}

func (s *Store) haveTile(pix int64) bool {
	_, err := os.Stat(s.tilePath(pix))
	return err == nil
}

func (s *Store) fileDone(name string) bool {
	_, err := os.Stat(filepath.Join(s.Dir, "done-"+name))
	return err == nil
}

func (s *Store) writeTile(pix int64, stars []Star) error {
	buf := make([]byte, len(stars)*starSize)
	for i, st := range stars {
		b := buf[i*starSize:]
		binary.LittleEndian.PutUint64(b[0:], uint64(st.SourceID))
		binary.LittleEndian.PutUint64(b[8:], math.Float64bits(st.RA))
		binary.LittleEndian.PutUint64(b[16:], math.Float64bits(st.Dec))
		binary.LittleEndian.PutUint32(b[24:], math.Float32bits(st.Parallax))
		binary.LittleEndian.PutUint32(b[28:], math.Float32bits(st.PMRA))
		binary.LittleEndian.PutUint32(b[32:], math.Float32bits(st.PMDec))
		binary.LittleEndian.PutUint32(b[36:], math.Float32bits(st.G))
	}
	p := s.tilePath(pix)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, buf, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// readTile returns the sources of a pixel; a missing tile in a finished file
// means the pixel has none.
func (s *Store) readTile(pix int64) ([]Star, error) {
	buf, err := os.ReadFile(s.tilePath(pix))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	stars := make([]Star, len(buf)/starSize)
	for i := range stars {
		b := buf[i*starSize:]
		stars[i] = Star{
			SourceID: int64(binary.LittleEndian.Uint64(b[0:])),
			RA:       math.Float64frombits(binary.LittleEndian.Uint64(b[8:])),
			Dec:      math.Float64frombits(binary.LittleEndian.Uint64(b[16:])),
			Parallax: math.Float32frombits(binary.LittleEndian.Uint32(b[24:])),
			PMRA:     math.Float32frombits(binary.LittleEndian.Uint32(b[28:])),
			PMDec:    math.Float32frombits(binary.LittleEndian.Uint32(b[32:])),
			G:        math.Float32frombits(binary.LittleEndian.Uint32(b[36:])),
		}
	}
	return stars, nil
}

var fileRE = regexp.MustCompile(`GaiaSource_(\d+)-(\d+)\.csv\.gz$`)

// index lists the bulk files and their pixel ranges, from disk after the
// first time.
func (s *Store) index(ctx context.Context) ([]sourceFile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.files != nil {
		return s.files, nil
	}
	path := filepath.Join(s.Dir, "index.json")
	if data, err := os.ReadFile(path); err == nil && json.Unmarshal(data, &s.files) == nil && len(s.files) > 0 {
		return s.files, nil
	}
	var files []sourceFile
	marker := ""
	for {
		u := s.ListURL
		if marker != "" {
			u += "&marker=" + url.QueryEscape(marker)
		}
		page, err := s.list(ctx, u)
		if err != nil {
			return nil, err
		}
		for _, c := range page.Contents {
			m := fileRE.FindStringSubmatch(c.Key)
			if m == nil {
				continue
			}
			lo, _ := strconv.ParseInt(m[1], 10, 64)
			hi, _ := strconv.ParseInt(m[2], 10, 64)
			files = append(files, sourceFile{Name: filepath.Base(c.Key), Lo: lo, Hi: hi, Size: c.Size})
		}
		if !page.IsTruncated || len(page.Contents) == 0 {
			break
		}
		marker = page.Contents[len(page.Contents)-1].Key
	}
	if len(files) == 0 {
		return nil, errors.New("gaia: the bulk file listing is empty")
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Lo < files[j].Lo })
	if data, err := json.Marshal(files); err == nil {
		os.WriteFile(path, data, 0o644)
	}
	s.files = files
	return files, nil
}

type listing struct {
	IsTruncated bool
	Contents    []struct {
		Key  string
		Size int64
	}
}

func (s *Store) list(ctx context.Context, u string) (listing, error) {
	var l listing
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return l, err
	}
	res, err := s.HTTP.Do(req)
	if err != nil {
		return l, fmt.Errorf("gaia: listing: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return l, fmt.Errorf("gaia: listing: HTTP %d", res.StatusCode)
	}
	err = xml.NewDecoder(res.Body).Decode(&l)
	return l, err
}

func separation(ra1, dec1, ra2, dec2 float64) float64 {
	const d = math.Pi / 180
	a := math.Sin((dec2 - dec1) * d / 2)
	b := math.Sin((ra2 - ra1) * d / 2)
	h := a*a + math.Cos(dec1*d)*math.Cos(dec2*d)*b*b
	return 2 * math.Asin(math.Sqrt(math.Min(1, h))) / d
}
