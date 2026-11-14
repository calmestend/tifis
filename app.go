package main

import (
	"encoding/json"
	"math"
	"sync"
	"time"

	"spherex/internal/sky"
)

type OpenRequest struct {
	RA   float64 `json:"ra"`
	Dec  float64 `json:"dec"`
	FOV  float64 `json:"fov"`
	Name string  `json:"name"`
	// Real asks for SPHEREx images from the archive instead of the simulation.
	Real bool `json:"real"`
}

type ViewState struct {
	Band    int    `json:"band"`
	Mode    string `json:"mode"` // epoch, diff or sum
	Visit   int    `json:"visit"`
	Ref     int    `json:"ref"` // -1 picks the reference automatically
	Flags   bool   `json:"flags"`
	Catalog bool   `json:"catalog"`
	Tint    bool   `json:"tint"`
}

type stretch struct {
	ready         bool
	median, sigma float64
	high          float64
}

// Selection is a source picked on the viewer; the server measures it.
type Selection struct {
	X, Y        float64 // where it was picked, image pixels
	Visit, Band int
	Motion      *sky.Motion
	busy        bool // a measurement is on its way
}

type App struct {
	mu    sync.Mutex
	queue []func()

	emit func(kind string, payload any)
	// api is the origin of the server, which does the science.
	api string
	// gen changes with every Open, so answers for an older field are dropped.
	gen int

	field   *sky.Field
	view    ViewState
	stretch [sky.NumChannels]stretch
	loader  loader
	sel     *Selection
	viewer  *Viewer

	// Result of the last composition, used by the viewer and hover readout.
	shown      []float32
	shownFlags []sky.Flag
	shownInfo  FrameInfo
	dirty      bool
	selDirty   bool
	selTimer   float64
}

func NewApp() *App {
	return &App{
		emit:   func(string, any) {},
		view:   ViewState{Band: 2, Mode: "epoch", Ref: -1, Catalog: false},
		viewer: NewViewer(),
	}
}

// Post queues work from UI callbacks so it runs on the render loop.
func (a *App) Post(f func()) {
	a.mu.Lock()
	a.queue = append(a.queue, f)
	a.mu.Unlock()
}

func (a *App) drain() {
	a.mu.Lock()
	q := a.queue
	a.queue = nil
	a.mu.Unlock()
	for _, f := range q {
		f()
	}
}

// announce tells the page the engine is ready, with the test objects at
// today's position as the server lists them.
func (a *App) announce() {
	go func() {
		body, err := a.get("/api/targets")
		targets := json.RawMessage("[]")
		if err == nil && json.Valid(body) {
			targets = body
		}
		a.Post(func() { a.emit("ready", map[string]any{"targets": targets}) })
	}()
}

type visitInfo struct {
	MJD    float64 `json:"mjd"`
	Date   string  `json:"date"`
	Season int     `json:"season"`
	Sub    int     `json:"sub"`
}

type channelInfo struct {
	Lo        float64 `json:"lo"`
	Hi        float64 `json:"hi"`
	Available []bool  `json:"available"`
}

type fieldInfo struct {
	TargetID string        `json:"target"`
	Name     string        `json:"name"`
	RA       float64       `json:"ra"`
	Dec      float64       `json:"dec"`
	FOV      float64       `json:"fov"`
	N        int           `json:"npix"`
	PixScale float64       `json:"pixscale"`
	Real     bool          `json:"real"`
	FluxUnit string        `json:"fluxUnit"`
	Visits   []visitInfo   `json:"visits"`
	Channels []channelInfo `json:"channels"`
}

func (a *App) Open(req OpenRequest) {
	a.gen++
	// A fast mover's current position changes the 6-decimal center every few
	// hours; rounding to 1.8" keeps the requests, and so the server cache and
	// its warm-up entries, the same for weeks.
	req.RA = math.Round(req.RA*2000) / 2000
	req.Dec = math.Round(req.Dec*2000) / 2000
	a.openField(req, math.Max(4, math.Min(12, req.FOV)))
}

func (a *App) startField(f *sky.Field, req OpenRequest, fov float64) {
	a.field = f
	a.stretch = [sky.NumChannels]stretch{}
	a.sel = nil
	a.shown = nil
	a.loader.reset(f)
	a.viewer.Reset(f.N)
	a.dirty = true
	a.view.Visit = min(a.view.Visit, max(0, len(f.Visits)-1))

	info := fieldInfo{
		TargetID: f.TargetID, Name: f.Name, RA: req.RA, Dec: req.Dec, FOV: fov,
		N: f.N, PixScale: sky.PixScale, Real: f.Real, FluxUnit: f.FluxUnit,
	}
	for _, v := range f.Visits {
		info.Visits = append(info.Visits, visitInfo{v.MJD, sky.MJDToTime(v.MJD).Format("2006-01-02"), v.Season, int(math.Round(v.SubBand))})
	}
	for c, ch := range sky.Channels {
		ci := channelInfo{Lo: ch.Lo, Hi: ch.Hi}
		for _, fr := range f.Frames[c] {
			ci.Available = append(ci.Available, fr.Available)
		}
		info.Channels = append(info.Channels, ci)
	}
	a.emit("field", info)
	a.emit("select", nil)
}

func (a *App) SetView(v ViewState) {
	if v.Band < 0 || v.Band >= sky.NumChannels {
		v.Band = a.view.Band
	}
	if a.field != nil {
		v.Visit = max(0, min(v.Visit, len(a.field.Visits)-1))
	}
	a.view = v
	a.dirty = true
}

// loader brings frames in nearest-first around the playhead in the current
// channel, a few at a time, so playback can start right away.
type loader struct {
	pending  []*sky.Frame
	total    int
	done     int
	inflight int
	// Frames whose download failed for a passing reason, back in the queue
	// after a delay; attempts counts the tries per frame.
	retry    []retryItem
	attempts map[*sky.Frame]int
}

type retryItem struct {
	frame *sky.Frame
	at    time.Time
}

// Browsers open about six connections per host.
const maxInflight = 6

func (l *loader) reset(f *sky.Field) {
	l.pending = l.pending[:0]
	l.retry = l.retry[:0]
	l.attempts = map[*sky.Frame]int{}
	l.done, l.inflight = 0, 0
	for c := range sky.NumChannels {
		for _, fr := range f.Frames[c] {
			if !fr.Available {
				fr.Loaded = true
				continue
			}
			l.pending = append(l.pending, fr)
		}
	}
	l.total = len(l.pending)
}

func (l *loader) next(v ViewState) *sky.Frame {
	best, score := -1, math.MaxInt
	for i, fr := range l.pending {
		s := abs(fr.Visit - v.Visit)
		if fr.Channel != v.Band {
			s += 10000
		}
		if s < score {
			best, score = i, s
		}
	}
	if best < 0 {
		return nil
	}
	fr := l.pending[best]
	l.pending[best] = l.pending[len(l.pending)-1]
	l.pending = l.pending[:len(l.pending)-1]
	return fr
}

type loadedItem struct {
	Channel   int     `json:"c"`
	Visit     int     `json:"v"`
	Lambda    float64 `json:"l"`
	Available bool    `json:"a"`
}

type loadedEvent struct {
	Items []loadedItem `json:"items"`
	Done  int          `json:"done"`
	Total int          `json:"total"`
}

func (a *App) frameLoaded(fr *sky.Frame, ev *loadedEvent) {
	a.loader.done++
	ev.Items = append(ev.Items, loadedItem{fr.Channel, fr.Visit, fr.Lambda, fr.Available})
	ev.Done, ev.Total = a.loader.done, a.loader.total
	a.initStretch(fr)
	if fr.Channel == a.view.Band || a.view.Mode == "diff" {
		a.dirty = true
	}
	a.selDirty = true
}

func (a *App) Update(dt float64) {
	a.drain()
	if a.field == nil {
		return
	}

	l := &a.loader
	now := time.Now()
	kept := l.retry[:0]
	for _, r := range l.retry {
		if now.After(r.at) {
			l.pending = append(l.pending, r.frame)
		} else {
			kept = append(kept, r)
		}
	}
	l.retry = kept
	for l.inflight < maxInflight && len(l.pending) > 0 {
		l.inflight++
		a.fetchFrame(l.next(a.view))
	}

	if a.dirty {
		a.compose()
		a.dirty = false
		if a.viewer.hoverX >= 0 {
			a.hover(a.viewer.hoverX, a.viewer.hoverY)
		}
	}

	// New frames or catalog entries change the measurement; ask again at most
	// once a second, one request at a time.
	a.selTimer -= dt
	if a.sel != nil && a.selDirty && !a.sel.busy && a.selTimer <= 0 {
		a.measureSelection()
	}
}

func (a *App) initStretch(fr *sky.Frame) {
	st := &a.stretch[fr.Channel]
	if st.ready || !fr.Available {
		return
	}
	st.median, st.sigma = fr.Background, fr.Sigma
	st.high = math.Max(sky.Percentile(fr.Image, fr.Flags, 0.998)-st.median, 30*st.sigma)
	st.ready = true
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
