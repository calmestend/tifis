import { run } from './raylib-go.js';
import { t, setLang, getLang, detectLang } from './i18n.js';
import { parseCoords, formatRA, formatDec } from './coords.js';

// Okabe-Ito, short to long wavelength; the engine uses the same colors.
const COLORS = ['#0072B2', '#56B4E9', '#009E73', '#F0E442', '#E69F00', '#D55E00'];
const LEVELS = ['explore', 'compare', 'analyze'];
const MODES = ['epoch', 'diff', 'sum'];
const FEATURES = [
  { key: 'feature.h2o', l: 3.05, w: 0.18 },
  { key: 'feature.ch4', l: 3.32, w: 0.08 },
  { key: 'feature.co2', l: 4.27, w: 0.06 },
  { key: 'feature.co', l: 4.67, w: 0.07 },
];
// Sesame mirrors that answer with CORS headers; the main CDS URL redirects
// without them, so the browser cannot follow it.
const SESAME = [
  'https://vizier.cfa.harvard.edu/viz-bin/nph-sesame/-oI/SNV?',
  'https://vizier.idia.ac.za/viz-bin/nph-sesame/-oI/SNV?',
];
const PIX = 6.15;
const SVGNS = 'http://www.w3.org/2000/svg';

const $ = (id) => document.getElementById(id);
const cssVar = (name) => getComputedStyle(document.documentElement).getPropertyValue(name).trim();
// SVG drawn from JS takes its colors from the CSS theme.
const THEME = { accent: cssVar('--accent'), text: cssVar('--text'), lineStrong: cssVar('--line-strong') };
const reduceMotion = matchMedia('(prefers-reduced-motion: reduce)').matches;

const state = {
  ready: false,
  targets: [],
  field: null,
  request: null,
  loaded: [],
  done: 0,
  total: 0,
  view: { band: 2, mode: 'epoch', visit: 0, ref: -1, flags: false, catalog: false, tint: false },
  level: 'explore',
  fov: 6,
  frame: null,
  selection: null,
  hover: null,
  playing: false,
  speed: 1,
  step: 'season',
  embed: false,
  specLog: true,
  // Real SPHEREx images through the proxy; false falls back to the simulation.
  real: false,
  // Wavelength of each loaded frame, [channel][visit], for season stepping.
  lambda: [],
  status: {},
};

const engine = () => globalThis.skyEngine;

// ---------- Engine events ----------

globalThis.skyUI = {
  event(kind, json) {
    const data = JSON.parse(json);
    handlers[kind]?.(data);
  },
};

const handlers = {
  async ready({ targets }) {
    state.real = await archiveAvailable;
    state.ready = true;
    state.targets = targets;
    renderBanner();
    engine().resize(Math.floor($('viewer').clientWidth), Math.floor($('viewer').clientHeight));
    renderCards();
    renderDatalist();
    startFromURL();
  },
  field(f) {
    state.field = f;
    state.loaded = f.channels.map((c) => c.available.map(() => false));
    state.lambda = f.channels.map((c) => c.available.map(() => 0));
    state.done = 0;
    state.total = f.channels.length * f.visits.length;
    state.view.visit = Math.min(state.view.visit, f.visits.length - 1);
    if (state.view.ref >= f.visits.length) state.view.ref = -1;
    state.selection = null;
    state.frame = null;
    $('welcome').hidden = true;
    renderBanner();
    renderTitle();
    renderChannels();
    renderFovCost();
    layoutTimeline();
    renderSelection();
    snapVisit();
    sendView();
    if (state.embed) $('embed-open').href = shareURL().replace(/[?&]embed=1/, '');
    if (state.autoplay && !reduceMotion) setPlaying(true);
    state.autoplay = false;
  },
  loaded({ items, done, total }) {
    for (const it of items) {
      state.loaded[it.c][it.v] = true;
      state.lambda[it.c][it.v] = it.l;
      // A real cutout that could not be read turns into a gap.
      state.field.channels[it.c].available[it.v] = it.a;
    }
    state.done = done;
    state.total = total;
    schedule('timeline', renderTimeline);
    schedule('channels', renderChannelCounts);
    schedule('status', renderStatus);
  },
  frame(fr) {
    state.frame = fr;
    renderFrame();
    renderTimeline();
  },
  hover(h) {
    state.hover = h;
    renderHover();
  },
  select(sel) {
    state.selection = sel;
    renderSelection();
    renderTimeline();
  },
  status(st) {
    state.status[st.stage] = st;
    if (st.stage === 'visits' && st.state === 'loading') {
      state.field = null;
      state.statusSince = performance.now();
      $('timeline-svg').replaceChildren();
    }
    renderArchiveStatus();
  },
};

const scheduled = new Set();
function schedule(key, fn) {
  if (scheduled.has(key)) return;
  scheduled.add(key);
  requestAnimationFrame(() => {
    scheduled.delete(key);
    fn();
  });
}

// ---------- State changes ----------

function effectiveView() {
  const v = { ...state.view };
  if (state.level === 'explore') {
    v.mode = 'epoch';
    v.catalog = false;
  }
  if (state.level !== 'analyze') v.flags = false;
  return v;
}

function sendView() {
  if (!state.field) return;
  engine()?.view(JSON.stringify(effectiveView()));
  renderControls();
  schedule('url', writeURL);
}

function setVisit(v) {
  if (!state.field) return;
  const n = state.field.visits.length;
  state.view.visit = ((v % n) + n) % n;
  sendView();
}

function setBand(b) {
  state.view.band = Math.max(0, Math.min(5, b));
  snapVisit();
  sendView();
  renderChannels();
}

// Not every detector has every visit (real exposures cover two of the six);
// move to the closest visit in time that has an image in this channel.
function snapVisit() {
  const f = state.field;
  if (f && !f.channels[state.view.band].available[state.view.visit]) {
    const avail = f.channels[state.view.band].available;
    for (let d = 1; d < avail.length; d++) {
      const i = [state.view.visit - d, state.view.visit + d].find((j) => avail[j]);
      if (i !== undefined) {
        state.view.visit = i;
        break;
      }
    }
  }
}

function setMode(m) {
  if (!MODES.includes(m)) return;
  state.view.mode = m;
  if (m !== 'epoch' && state.level === 'explore') setLevel('compare');
  sendView();
}

function setLevel(l) {
  if (!LEVELS.includes(l)) return;
  state.level = l;
  document.body.dataset.level = l;
  for (const b of document.querySelectorAll('.levels button')) b.setAttribute('aria-checked', b.dataset.level === l);
  sendView();
  renderSelection();
  renderTimeline();
  renderFrame();
}

function setPlaying(p) {
  state.playing = p && !!state.field;
  const btn = $('play');
  btn.setAttribute('aria-pressed', state.playing);
  btn.setAttribute('aria-label', t(state.playing ? 'play.pause' : 'play.play'));
  if (state.playing) {
    lastTick = 0;
    acc = 0;
    requestAnimationFrame(tick);
  }
}

// nextVisit steps through visits that have an image in the current channel.
// By season, it jumps to the visit of the next season with the closest
// wavelength, so consecutive frames share the same color. Real frames only
// reveal their wavelength once loaded; until then the sub-band estimate is used.
function nextVisit(dir, step = state.step) {
  const f = state.field;
  const band = state.view.band;
  const n = f.visits.length;
  const avail = (i) => f.channels[band].available[i];
  const cur = f.visits[state.view.visit];
  if (step === 'visit') {
    for (let k = 1; k <= n; k++) {
      const i = (((state.view.visit + dir * k) % n) + n) % n;
      if (avail(i)) return i;
    }
    return state.view.visit;
  }
  const seasons = f.visits[n - 1].season + 1;
  const ch = f.channels[band];
  const color = (i) => {
    const l = state.lambda[band]?.[i];
    if (l) return ((l - ch.lo) / (ch.hi - ch.lo)) * 17;
    return f.visits[i].sub >= 0 ? f.visits[i].sub : null;
  };
  const here = color(state.view.visit);
  for (let k = 1; k <= seasons; k++) {
    const s = (((cur.season + dir * k) % seasons) + seasons) % seasons;
    let best = -1, bestD = Infinity;
    f.visits.forEach((v, i) => {
      if (v.season !== s || !avail(i)) return;
      const c = color(i);
      const dc = here == null || c == null ? 500 : Math.abs(c - here);
      const d = dc * 1000 + Math.abs(v.mjd - cur.mjd) / 1000;
      if (d < bestD) { best = i; bestD = d; }
    });
    if (best >= 0) return best;
  }
  return state.view.visit;
}

let lastTick = 0;
let acc = 0;
function tick(ts) {
  if (!state.playing) return;
  const dt = lastTick ? (ts - lastTick) / 1000 : 0;
  lastTick = ts;
  acc += dt;
  const interval = (state.step === 'season' ? 0.9 : 0.25) / state.speed;
  if (acc >= interval) {
    const next = nextVisit(1);
    // Wait for the frame instead of skipping it: the timeline shows what is loaded.
    if (state.loaded[state.view.band][next]) {
      acc = 0;
      setVisit(next);
    }
  }
  requestAnimationFrame(tick);
}

// ---------- Opening fields ----------

function open(req, opts = {}) {
  state.request = { ra: req.ra, dec: req.dec, fov: state.fov, name: req.name || '', real: state.real };
  state.autoplay = opts.autoplay ?? true;
  if (opts.view) Object.assign(state.view, opts.view);
  setPlaying(false);
  if (!state.ready) return;
  $('welcome').hidden = true;
  state.field = null;
  state.status = {};
  renderArchiveStatus();
  renderTitle();
  // The engine starts downloading right away, nearest the playhead in the
  // current channel, so it needs the view first.
  engine().view(JSON.stringify(effectiveView()));
  engine().open(JSON.stringify(state.request));
}

function openTarget(tg, opts = {}) {
  const view = { band: tg.band, visit: 0, ref: -1 };
  if (state.level !== 'explore') view.mode = tg.mode;
  open({ ra: tg.ra, dec: tg.dec, name: tg.name }, { ...opts, view: { ...view, ...opts.view } });
}

const normalize = (s) => s.toLowerCase().normalize('NFD').replace(/[\u0300-\u036f]/g, '').replace(/[’'`]/g, '').replace(/\s+/g, ' ').trim();

function findTarget(q) {
  const n = normalize(q);
  return state.targets.find((tg) => [tg.id, tg.name, ...tg.aliases].some((a) => normalize(a) === n));
}

async function search(q) {
  q = q.trim();
  if (!q) return;
  const c = parseCoords(q);
  if (c) return open({ ...c, name: '' });
  const tg = findTarget(q);
  if (tg) return openTarget(tg);

  toast(t('search.resolving', { q }));
  const text = await resolveName(q);
  if (text == null) return toast(t('search.offline'));
  const m = /%J\s+([\d.]+)\s+([+-][\d.]+)/.exec(text);
  if (!m) return toast(t('search.notfound', { q }));
  let ra = parseFloat(m[1]);
  let dec = parseFloat(m[2]);
  // Sesame gives J2000 positions at epoch J2000; carry fast movers to today.
  const pm = /%P\s+([+-]?[\d.]+)\s+([+-]?[\d.]+)/.exec(text);
  if (pm) {
    const yr = (Date.now() - Date.UTC(2000, 0, 1, 12)) / 3.15576e10;
    dec += (parseFloat(pm[2]) * yr) / 3.6e6;
    ra += (parseFloat(pm[1]) * yr) / 3.6e6 / Math.cos((dec * Math.PI) / 180);
  }
  hideToast();
  open({ ra, dec, name: q });
}

async function resolveName(q) {
  for (const base of SESAME) {
    try {
      const res = await fetch(base + encodeURIComponent(q), { signal: AbortSignal.timeout(5000) });
      if (res.ok) return await res.text();
    } catch {
      // Try the next mirror.
    }
  }
  return null;
}

// ---------- Archive ----------

// The proxy that relays IRSA serves the app, so /api answering means real data.
async function detectArchive() {
  if (new URLSearchParams(location.search).get('data') === 'sim') return false;
  try {
    const res = await fetch('/api/health', { signal: AbortSignal.timeout(2000) });
    return res.ok;
  } catch {
    return false;
  }
}

function renderBanner() {
  const real = state.real;
  $('banner').classList.toggle('real', real);
  $('banner-title').textContent = t(real ? 'real.title' : 'sim.title');
  $('banner-body').textContent = t(real ? 'real.body' : 'sim.body');
}

let archiveTimer;
let gaiaTimer;
function renderArchiveStatus() {
  const v = state.status.visits;
  const box = $('archive-status');
  clearInterval(archiveTimer);
  box.hidden = !v || v.state === 'done' || !state.real;
  if (!box.hidden) {
    const loading = v.state === 'loading';
    $('archive-spinner').hidden = !loading;
    $('archive-sim').hidden = loading;
    $('archive-text').textContent = t(`archive.${v.state}`);
    const detail = () => {
      $('archive-detail').textContent = loading
        ? t('archive.slow', { s: Math.round((performance.now() - state.statusSince) / 1000) })
        : v.error || '';
    };
    detail();
    if (loading) archiveTimer = setInterval(detail, 1000);
  }
  const c = state.status.catalog;
  clearInterval(gaiaTimer);
  const text = !state.real || !c ? ''
    : c.state === 'loading' ? t('catalog.loading')
    : c.state === 'error' ? t('catalog.error', { e: c.error })
    : c.missing?.length ? t('catalog.partial', { n: c.count, c: c.missing.join(', ') })
    : t('catalog.done', { n: c.count });
  $('catalog-status').textContent = text;
  // Gaia comes from 230 MB bulk files the first time a region is seen;
  // show how far the download has got.
  if (state.real && c?.state === 'loading') {
    const poll = async () => {
      try {
        const r = state.request;
        const files = await (await fetch(`/api/gaia/progress?ra=${r.ra}&dec=${r.dec}`)).json();
        if (state.status.catalog?.state !== 'loading') return;
        $('catalog-status').textContent = files.length
          ? t('catalog.gaia', { p: Math.round(100 * Math.max(...files.map((f) => f.fraction))), mb: Math.round(files[0].mb) })
          : text;
      } catch {
        // The plain message stays.
      }
    };
    poll();
    gaiaTimer = setInterval(poll, 2000);
  }
}

// ---------- URL ----------

function startFromURL() {
  const p = new URLSearchParams(location.search);
  state.embed = p.get('embed') === '1';
  document.body.classList.toggle('embed', state.embed);
  if (p.has('level')) setLevel(p.get('level'));
  if (p.has('fov')) state.fov = clamp(parseFloat(p.get('fov')) || 6, 4, 12);
  $('fov').value = state.fov;
  $('fov-value').textContent = state.fov + t('unit.arcmin');
  const view = {};
  if (p.has('band')) view.band = clamp(parseInt(p.get('band'), 10) - 1 || 0, 0, 5);
  if (MODES.includes(p.get('mode'))) view.mode = p.get('mode');
  if (p.has('t')) view.visit = Math.max(0, parseInt(p.get('t'), 10) || 0);
  if (p.has('ref')) view.ref = parseInt(p.get('ref'), 10);
  view.flags = p.get('flags') === '1';
  view.catalog = p.get('catalog') === '1';
  view.tint = p.get('tint') === '1';
  $('tint').checked = view.tint;
  $('flags').checked = view.flags;
  $('catalog').checked = view.catalog;
  const autoplay = p.get('play') !== '0';

  const ra = parseFloat(p.get('ra'));
  const dec = parseFloat(p.get('dec'));
  if (Number.isFinite(ra) && Number.isFinite(dec)) {
    open({ ra, dec, name: p.get('name') || '' }, { view, autoplay });
  } else if (p.has('target') && findTarget(p.get('target'))) {
    openTarget(findTarget(p.get('target')), { view, autoplay });
  } else if (state.embed) {
    openTarget(state.targets[0], { view, autoplay });
  } else {
    $('welcome').hidden = false;
    renderTitle();
  }
}

function shareURL({ embed = false } = {}) {
  const r = state.request;
  const v = state.view;
  const p = new URLSearchParams();
  if (r) {
    p.set('ra', r.ra.toFixed(5));
    p.set('dec', r.dec.toFixed(5));
    p.set('fov', String(state.fov));
    if (state.field?.name) p.set('name', state.field.name);
    p.set('band', String(v.band + 1));
    p.set('mode', v.mode);
    p.set('t', String(v.visit));
    if (v.ref >= 0) p.set('ref', String(v.ref));
    if (v.flags) p.set('flags', '1');
    if (v.catalog) p.set('catalog', '1');
    if (v.tint) p.set('tint', '1');
  }
  if (state.level !== 'explore') p.set('level', state.level);
  if (getLang() !== detectLang()) p.set('lang', getLang());
  if (new URLSearchParams(location.search).get('data') === 'sim') p.set('data', 'sim');
  if (embed) p.set('embed', '1');
  return `${location.origin}${location.pathname}?${p}`;
}

function writeURL() {
  if (state.embed) return;
  history.replaceState(null, '', shareURL());
}

// ---------- Rendering ----------

function renderCards() {
  const ul = $('cards');
  ul.replaceChildren();
  for (const tg of state.targets) {
    const li = document.createElement('li');
    const b = document.createElement('button');
    b.className = 'card';
    b.style.setProperty('--card', COLORS[tg.band]);
    b.innerHTML = `<span class="tag"></span><strong></strong><span class="hook"></span><span class="look"></span>`;
    b.querySelector('.tag').textContent = t(`target.${tg.id}.tag`);
    b.querySelector('strong').textContent = tg.name;
    b.querySelector('.hook').textContent = t(`target.${tg.id}.hook`);
    b.querySelector('.look').textContent = t(`target.${tg.id}.look`);
    b.addEventListener('click', () => openTarget(tg));
    li.append(b);
    ul.append(li);
  }
}

function renderDatalist() {
  const dl = $('target-names');
  dl.replaceChildren(...state.targets.map((tg) => Object.assign(document.createElement('option'), { value: tg.name })));
}

function renderTitle() {
  const r = state.request;
  if (!r) {
    $('field-name').textContent = '';
    $('field-coords').textContent = '';
    return;
  }
  $('field-name').textContent = state.field?.name || r.name || `${formatRA(r.ra)} ${formatDec(r.dec)}`;
  $('field-coords').textContent = `${formatRA(r.ra)} ${formatDec(r.dec)} · ${state.fov}′ · 1 px = ${PIX}″`;
  document.title = `${$('field-name').textContent} · SPHEREx Sky Timeline`;
}

function renderChannels() {
  const box = $('channels');
  const f = state.field;
  if (!box.children.length) {
    for (let i = 0; i < 6; i++) {
      const b = document.createElement('button');
      b.className = 'channel';
      b.setAttribute('role', 'radio');
      b.style.setProperty('--ch', COLORS[i]);
      b.innerHTML = `<span class="bar"></span><span><span class="name"></span><span class="tech"></span></span><span class="count"></span>`;
      b.addEventListener('click', () => setBand(i));
      b.addEventListener('keydown', (e) => {
        if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
          e.preventDefault();
          e.stopPropagation();
          setBand(i + (e.key === 'ArrowDown' ? 1 : -1));
          box.children[state.view.band].focus();
        }
      });
      box.append(b);
    }
  }
  const ranges = f ? f.channels : [[0.75, 1.1], [1.1, 1.6], [1.6, 2.4], [2.4, 3.8], [3.8, 4.4], [4.4, 5.0]].map(([lo, hi]) => ({ lo, hi }));
  [...box.children].forEach((b, i) => {
    const on = i === state.view.band;
    b.setAttribute('aria-checked', on);
    b.tabIndex = on ? 0 : -1;
    b.querySelector('.name').textContent = t(`channel.${i + 1}`);
    b.querySelector('.tech').textContent = `${t('channel.n', { n: i + 1 })} · ${fmtRange(ranges[i].lo, ranges[i].hi)}`;
  });
  renderChannelCounts();
}

function renderChannelCounts() {
  const f = state.field;
  [...$('channels').children].forEach((b, i) => {
    const c = b.querySelector('.count');
    if (!f) return (c.textContent = '');
    const avail = f.channels[i].available.filter(Boolean).length;
    const loaded = state.loaded[i].filter((x, v) => x && f.channels[i].available[v]).length;
    c.textContent = loaded < avail ? `${loaded}/${avail}` : `${avail}`;
    c.title = t('timeline.loaded', { done: loaded, total: avail });
  });
}

function renderControls() {
  const v = state.view;
  for (const b of $('modes').children) b.setAttribute('aria-checked', b.dataset.mode === v.mode);
  $('mode-hint').textContent = t(`mode.${v.mode}.hint`);
  $('ref-row').hidden = v.mode === 'epoch';
  $('ref-auto').hidden = v.ref < 0;
  renderRefLabel();
  const tl = $('timeline');
  const f = state.field;
  if (f) {
    const vis = f.visits[v.visit];
    tl.setAttribute('aria-valuemax', f.visits.length - 1);
    tl.setAttribute('aria-valuenow', v.visit);
    tl.setAttribute('aria-valuetext', t('timeline.value', { n: v.visit + 1, total: f.visits.length, date: fmtDate(vis.date), s: vis.season + 1 }));
  }
}

function renderRefLabel() {
  const fr = state.frame;
  const v = state.view;
  const el = $('ref-label');
  if (!fr || !state.field || v.mode === 'epoch') return (el.textContent = '');
  if (fr.ref < 0) return (el.textContent = t('ref.none'));
  const vis = state.field.visits[fr.ref];
  const key = v.mode === 'diff' ? 'ref.label.diff' : 'ref.label.sum';
  el.textContent = `${t(key, { n: fr.ref + 1, date: fmtDate(vis.date), k: fr.stacked ?? 0 })} ${v.ref < 0 ? t('ref.auto.tag') : ''}`;
}

function renderFrame() {
  const fr = state.frame;
  const f = state.field;
  if (!fr || !f) return;
  const ch = f.channels[fr.band];
  const chip = $('frame-channel');
  chip.style.setProperty('--chip', COLORS[fr.band]);
  chip.textContent = `${t('channel.n', { n: fr.band + 1 })} · ${fmtRange(ch.lo, ch.hi)}`;
  $('frame-label').textContent = `${t('frame.label', { date: fmtDate(fr.date), n: fr.visit + 1, total: f.visits.length })} · λ ${fr.lambda.toFixed(2)} µm`;
  $('loading').hidden = fr.loaded || (fr.mode === 'epoch' && !fr.available);
  $('missing').hidden = !(fr.loaded && !fr.available && fr.mode !== 'sum');
  renderRefLabel();

  const rows = [
    ['meta.date', `${fr.date} · MJD ${fr.mjd.toFixed(3)}`],
    ['meta.lambda', `${fr.lambda.toFixed(3)} µm${fr.refLambda ? ` (A: ${fr.refLambda.toFixed(3)} µm)` : ''}`],
    ['meta.file', fr.file],
    ['meta.bg', fr.available ? `${fr.bg.toFixed(4)} MJy/sr` : '—'],
    ['meta.sigma', fr.available ? `${fr.sigma.toFixed(4)} MJy/sr` : '—'],
    ['meta.flagged', String(fr.flagged)],
    ['meta.pixel', `${PIX}″ · ${f.npix}×${f.npix}`],
  ];
  if (fr.stacked) rows.push(['meta.stacked', String(fr.stacked)]);
  fillFacts($('meta'), rows);
}

function renderHover() {
  const h = state.hover;
  const out = $('readout');
  const tip = $('tip');
  if (!h) {
    out.textContent = '';
    tip.hidden = true;
    return;
  }
  const val = h.value == null ? '' : ` · ${t('readout.value', { v: h.value.toFixed(4) })}`;
  out.textContent = `α ${formatRA(h.ra)}  δ ${formatDec(h.dec)}\n(${h.x}, ${h.y})${val}`;
  const showFlags = h.flags?.length && state.level === 'analyze' && state.view.flags;
  tip.hidden = !showFlags;
  if (showFlags) {
    tip.replaceChildren(...h.flags.map((name) => {
      const d = document.createElement('div');
      d.innerHTML = '<b></b> <span></span>';
      d.querySelector('b').textContent = name;
      d.querySelector('span').textContent = t(`flag.${name}`);
      return d;
    }));
  }
}

function renderStatus() {
  const f = state.field;
  if (!f) return;
  const fr = state.view.visit;
  $('date-label').textContent = t('timeline.value', { n: fr + 1, total: f.visits.length, date: fmtDate(f.visits[fr].date), s: f.visits[fr].season + 1 });
  $('load-label').textContent = state.done < state.total
    ? t('timeline.loaded', { done: state.done, total: state.total })
    : t('timeline.ready', { total: state.total, date: fmtDate(f.visits[f.visits.length - 1].date) });
}

function renderFovCost() {
  const fov = parseFloat($('fov').value);
  const px = Math.round((fov * 60) / PIX);
  const frames = state.field
    ? state.field.channels.reduce((n, c) => n + c.available.filter(Boolean).length, 0)
    : 360;
  // Cutouts arrive in detector orientation, so they span the field's diagonal.
  // Each has IMAGE, VARIANCE and ZODI (float32) and FLAGS (int32) plus about
  // 72 KB of headers; the proxy leaves out the PSF cube.
  const side = Math.ceil(px * Math.SQRT2 * 1.02);
  const mb = (frames * (side * side * 16 + 72000)) / 1e6;
  $('fov-cost').textContent = t('fov.cost', { n: frames, px, mb: mb < 10 ? mb.toFixed(1) : Math.round(mb) });
}

// ---------- Timeline ----------

let tl = { xs: [], width: 0 };

function layoutTimeline() {
  const f = state.field;
  const svg = $('timeline-svg');
  const width = svg.clientWidth || 600;
  if (!f) return;
  const n = f.visits.length;
  const seasons = f.visits[n - 1].season + 1;
  const gap = 4;
  const pad = 10;
  const unit = (width - 2 * pad) / Math.max(1, n - 1 + gap * (seasons - 1));
  tl = { width, unit, xs: f.visits.map((v, i) => pad + (i + gap * v.season) * unit) };
  renderTimeline();
  renderStatus();
}

function svgEl(name, attrs = {}, text) {
  const el = document.createElementNS(SVGNS, name);
  for (const [k, v] of Object.entries(attrs)) el.setAttribute(k, v);
  if (text != null) el.textContent = text;
  return el;
}

function renderTimeline() {
  const f = state.field;
  const svg = $('timeline-svg');
  if (!f || !tl.xs.length) return svg.replaceChildren();
  const band = state.view.band;
  const col = COLORS[band];
  const nodes = [];
  const w = Math.max(2, Math.min(8, tl.unit * 0.65));

  // Seasons
  let start = 0;
  for (let i = 1; i <= f.visits.length; i++) {
    if (i === f.visits.length || f.visits[i].season !== f.visits[start].season) {
      const x0 = tl.xs[start] - w;
      const x1 = tl.xs[i - 1] + w;
      nodes.push(svgEl('rect', { x: x0, y: 14, width: x1 - x0, height: 56, rx: 4, fill: THEME.text, 'fill-opacity': 0.04 }));
      nodes.push(svgEl('text', { x: x0 + 2, y: 10 }, fmtMonth(f.visits[start].date)));
      start = i;
    }
  }

  // Reference epoch and stacked range
  const fr = state.frame;
  const mode = effectiveView().mode;
  if (fr && mode !== 'epoch' && fr.ref >= 0) {
    const xr = tl.xs[fr.ref];
    if (mode === 'sum') {
      nodes.push(svgEl('rect', { x: xr - w / 2, y: 46, width: tl.xs[state.view.visit] - xr + w, height: 22, fill: THEME.accent, 'fill-opacity': 0.15 }));
    }
    nodes.push(svgEl('line', { x1: xr, x2: xr, y1: 16, y2: 70, stroke: THEME.accent, 'stroke-dasharray': '3 3' }));
    nodes.push(svgEl('text', { x: xr + 3, y: 24, style: `fill:${THEME.accent};font-weight:600` }, 'A'));
  }

  // Visits: filled when loaded, outlined while loading, faint when missing
  f.visits.forEach((v, i) => {
    const x = tl.xs[i] - w / 2;
    if (!f.channels[band].available[i]) {
      nodes.push(svgEl('rect', { x, y: 62, width: w, height: 4, fill: THEME.lineStrong }));
    } else if (state.loaded[band][i]) {
      nodes.push(svgEl('rect', { x, y: 48, width: w, height: 18, rx: 1.5, fill: col }));
    } else {
      nodes.push(svgEl('rect', { x: x + 0.5, y: 48.5, width: w - 1, height: 17, rx: 1.5, fill: 'none', stroke: col, 'stroke-opacity': 0.45 }));
    }
  });

  // Light curve of the selected source in this channel
  let lightcurve = false;
  const sel = state.selection;
  if (sel && state.level === 'analyze') {
    const pts = sel.measurements.filter((m) => m.c === band);
    if (pts.length > 1) {
      const fs = pts.map((m) => m.f);
      const lo = Math.min(...fs);
      const hi = Math.max(...fs);
      const y = (v) => 42 - ((v - lo) / (hi - lo || 1)) * 24;
      nodes.push(svgEl('polyline', { points: pts.map((m) => `${tl.xs[m.v]},${y(m.f)}`).join(' '), fill: 'none', stroke: THEME.accent, 'stroke-opacity': 0.5 }));
      for (const m of pts) nodes.push(svgEl('circle', { cx: tl.xs[m.v], cy: y(m.f), r: 2.2, fill: m.ok ? THEME.accent : 'none', stroke: THEME.accent }));
      lightcurve = true;
    }
  }
  $('lc-legend').hidden = !lightcurve;

  // Playhead
  const xp = tl.xs[state.view.visit];
  nodes.push(svgEl('line', { x1: xp, x2: xp, y1: 14, y2: 74, stroke: THEME.text, 'stroke-width': 2 }));
  nodes.push(svgEl('circle', { cx: xp, cy: 76, r: 6, fill: THEME.text, stroke: col, 'stroke-width': 2 }));

  svg.replaceChildren(...nodes);
  renderStatus();
}

function visitAt(clientX) {
  const rect = $('timeline-svg').getBoundingClientRect();
  const x = clientX - rect.left;
  let best = 0;
  tl.xs.forEach((xi, i) => {
    if (Math.abs(xi - x) < Math.abs(tl.xs[best] - x)) best = i;
  });
  return best;
}

// ---------- Selection: spectrum and motion ----------

function renderSelection() {
  const sel = state.selection;
  $('sel-chip').hidden = !sel || state.level === 'analyze';
  $('spectrum-empty').hidden = !!sel;
  $('spectrum-box').hidden = !sel;
  if (!sel) return;
  renderSpectrum();

  const [pmRA, pmDec] = [sel.pmra, sel.pmdec];
  const total = Math.hypot(pmRA, pmDec);
  const err = Math.hypot(sel.pmraErr, sel.pmdecErr);
  const rows = [['motion.position', `${formatRA(sel.ra)} ${formatDec(sel.dec)}`]];
  rows.push(['motion.measured', sel.moving
    ? `${t('motion.value', { v: `${total.toFixed(1)} ± ${err.toFixed(1)}`, dir: t(`dir.${compass(pmRA, pmDec)}`) })} · ${t('motion.px', { v: (total / PIX).toFixed(2) })}`
    : t('motion.none')]);
  rows.push(['motion.components', `μα* ${pmRA.toFixed(2)} ± ${sel.pmraErr.toFixed(2)} · μδ ${pmDec.toFixed(2)} ± ${sel.pmdecErr.toFixed(2)} ″/a`]);
  if (sel.catalog) {
    const ct = Math.hypot(sel.catalog.pmra, sel.catalog.pmdec);
    // Agreement within 3 sigma, with a floor for systematics at this pixel size.
    const dist = Math.hypot(pmRA - sel.catalog.pmra, pmDec - sel.catalog.pmdec);
    const agree = sel.moving && dist < 3 * err + 0.3;
    const note = !sel.moving ? '' : agree ? ` · ✓ ${t('motion.match')}` : ` · ${t('motion.differs', { d: dist.toFixed(1) })}`;
    rows.push(['motion.catalog', `${sel.catalog.name} · ${sel.catalog.pmra.toFixed(2)}, ${sel.catalog.pmdec.toFixed(2)} (${ct.toFixed(2)}″/a)${note}`, agree]);
  }
  rows.push(['motion.points', String(sel.track?.length ?? 0)]);
  const inBand = sel.measurements.filter((m) => m.c === sel.band);
  const flagged = inBand.filter((m) => !m.ok).length;
  if (flagged > inBand.length / 3) rows.push(['motion.caveat', t('motion.saturated', { n: flagged, total: inBand.length })]);
  fillFacts($('motion'), rows);
}

function renderSpectrum() {
  const sel = state.selection;
  const svg = $('spectrum');
  const W = svg.clientWidth || 300;
  const H = 200;
  const m = { l: 40, r: 6, t: 28, b: 30 };
  // Points whose aperture has too many flagged pixels (saturated cores,
  // cosmic rays) are kept but drawn hollow, like the bad-pixel counts of the
  // SPHEREx Source Discovery Tool.
  const pts = sel.measurements;
  const x = (l) => m.l + ((l - 0.7) / (5.1 - 0.7)) * (W - m.l - m.r);
  const log = state.specLog;
  const vals = pts.map((p) => p.f).filter((v) => !log || v > 0);
  const nodes = [];
  if (!vals.length) return svg.replaceChildren();
  let lo = Math.min(...vals);
  let hi = Math.max(...vals);
  if (log) {
    lo = Math.log10(Math.max(lo, hi * 1e-4));
    hi = Math.log10(hi);
  }
  const span = hi - lo || 1;
  lo -= span * 0.08;
  hi += span * 0.08;
  const y = (v) => {
    const vv = log ? Math.log10(Math.max(v, 10 ** lo)) : v;
    return m.t + (1 - (vv - lo) / (hi - lo)) * (H - m.t - m.b);
  };

  FEATURES.forEach((ft, i) => {
    const g = svgEl('g', { class: 'feature' });
    g.append(svgEl('rect', { x: x(ft.l - ft.w / 2), y: m.t, width: x(ft.l + ft.w / 2) - x(ft.l - ft.w / 2), height: H - m.t - m.b }));
    // Alternate rows so neighbouring bands (H₂O and CH₄) do not overlap.
    g.append(svgEl('text', { x: x(ft.l), y: i % 2 ? m.t - 3 : m.t - 13, 'text-anchor': 'middle' }, t(ft.key)));
    nodes.push(g);
  });
  state.field.channels.forEach((c, i) => {
    nodes.push(svgEl('rect', { x: x(c.lo), y: H - m.b + 1, width: x(c.hi) - x(c.lo) - 1, height: 4, fill: COLORS[i] }));
  });
  nodes.push(svgEl('line', { class: 'axis', x1: m.l, x2: W - m.r, y1: H - m.b, y2: H - m.b }));
  nodes.push(svgEl('line', { class: 'axis', x1: m.l, x2: m.l, y1: m.t, y2: H - m.b }));
  for (const l of [1, 2, 3, 4, 5]) nodes.push(svgEl('text', { x: x(l), y: H - m.b + 16, 'text-anchor': 'middle' }, String(l)));
  nodes.push(svgEl('text', { x: W - m.r, y: H - 2, 'text-anchor': 'end' }, t('spectrum.x')));
  if (state.field.fluxUnit) nodes.push(svgEl('text', { x: 2, y: m.t - 13 }, state.field.fluxUnit));
  for (const v of yTicks(lo, hi, log)) {
    nodes.push(svgEl('text', { x: m.l - 4, y: y(v) + 3, 'text-anchor': 'end' }, fmtFlux(v)));
    nodes.push(svgEl('line', { class: 'axis', x1: m.l, x2: W - m.r, y1: y(v), y2: y(v), 'stroke-opacity': 0.4 }));
  }

  for (const p of pts) {
    const cx = x(p.l);
    const below = log && p.f <= 0;
    if (!below && p.e > 0) {
      nodes.push(svgEl('line', { x1: cx, x2: cx, y1: y(p.f - p.e), y2: y(p.f + p.e), stroke: COLORS[p.c], 'stroke-opacity': 0.5 }));
    }
    const dot = svgEl('circle', { cx, cy: y(p.f), r: 2.6, fill: below || !p.ok ? 'none' : COLORS[p.c], stroke: COLORS[p.c] });
    const unit = state.field.fluxUnit ? ` ${state.field.fluxUnit}` : '';
    const note = p.ok ? '' : ` · ${t('spectrum.flagged')}`;
    dot.append(svgEl('title', {}, `${p.l.toFixed(3)} µm · ${p.f.toPrecision(4)} ± ${p.e.toPrecision(2)}${unit} · ${fmtDate(state.field.visits[p.v].date)}${note}`));
    nodes.push(dot);
  }
  svg.setAttribute('viewBox', `0 0 ${W} ${H}`);
  svg.replaceChildren(...nodes);
}

function yTicks(lo, hi, log) {
  if (log) {
    const mult = hi - lo < 2.5 ? [1, 2, 5] : [1];
    const out = [];
    for (let e = Math.floor(lo); e <= Math.ceil(hi); e++) {
      for (const k of mult) {
        const v = k * 10 ** e;
        if (Math.log10(v) >= lo && Math.log10(v) <= hi) out.push(v);
      }
    }
    return out;
  }
  const step = niceStep((hi - lo) / 4);
  const out = [];
  for (let v = Math.ceil(lo / step) * step; v <= hi; v += step) out.push(v);
  return out;
}

function niceStep(raw) {
  const p = 10 ** Math.floor(Math.log10(raw));
  const r = raw / p;
  return (r < 1.5 ? 1 : r < 3.5 ? 2 : r < 7.5 ? 5 : 10) * p;
}

// ---------- Helpers ----------

function fillFacts(dl, rows) {
  dl.replaceChildren();
  for (const [k, v, good] of rows) {
    const dd = Object.assign(document.createElement('dd'), { textContent: v });
    if (good) dd.className = 'good';
    dl.append(Object.assign(document.createElement('dt'), { textContent: t(k) }), dd);
  }
}

function compass(east, north) {
  const dirs = ['N', 'NE', 'E', 'SE', 'S', 'SW', 'W', 'NW'];
  const a = (Math.atan2(east, north) * 180) / Math.PI;
  return dirs[Math.round(((a + 360) % 360) / 45) % 8];
}

const clamp = (v, lo, hi) => Math.max(lo, Math.min(hi, v));
const fmtRange = (lo, hi) => `${lo.toFixed(2)}–${hi.toFixed(2)} µm`;
const fmtDate = (iso) => new Date(`${iso}T12:00:00Z`).toLocaleDateString(getLang(), { day: 'numeric', month: 'short', year: 'numeric', timeZone: 'UTC' });
const fmtMonth = (iso) => new Date(`${iso}T12:00:00Z`).toLocaleDateString(getLang(), { month: 'short', year: 'numeric', timeZone: 'UTC' });
const fmtFlux = (v) => (Math.abs(v) >= 100 ? v.toFixed(0) : Math.abs(v) >= 1 ? v.toFixed(1) : v.toPrecision(1));

let toastTimer;
function toast(msg, action) {
  const el = $('toast');
  el.replaceChildren(document.createTextNode(msg));
  if (action) {
    const b = Object.assign(document.createElement('button'), { className: 'link', textContent: action.label });
    b.addEventListener('click', action.run);
    el.append(' ', b);
  }
  el.hidden = false;
  clearTimeout(toastTimer);
  toastTimer = setTimeout(hideToast, 4000);
}

function hideToast() {
  $('toast').hidden = true;
}

async function copy(text, msg) {
  try {
    await navigator.clipboard.writeText(text);
    toast(msg);
  } catch {
    prompt('', text);
  }
}

// ---------- Wiring ----------

function wire() {
  $('search').addEventListener('submit', (e) => {
    e.preventDefault();
    search($('q').value);
    $('q').blur();
  });
  for (const b of document.querySelectorAll('.levels button')) b.addEventListener('click', () => setLevel(b.dataset.level));
  for (const b of $('modes').children) b.addEventListener('click', () => setMode(b.dataset.mode));

  $('play').addEventListener('click', () => setPlaying(!state.playing));
  $('prev').addEventListener('click', () => { setPlaying(false); setVisit(nextVisit(-1, 'visit')); });
  $('next').addEventListener('click', () => { setPlaying(false); setVisit(nextVisit(1, 'visit')); });
  $('speed').addEventListener('change', (e) => { state.speed = parseFloat(e.target.value); });
  $('step').addEventListener('change', (e) => { state.step = e.target.value; });

  $('tint').addEventListener('change', (e) => { state.view.tint = e.target.checked; sendView(); });
  $('flags').addEventListener('change', (e) => { state.view.flags = e.target.checked; sendView(); renderHover(); });
  $('catalog').addEventListener('change', (e) => { state.view.catalog = e.target.checked; sendView(); });
  $('spec-log').addEventListener('change', (e) => { state.specLog = e.target.checked; renderSpectrum(); });
  $('ref-set').addEventListener('click', () => { state.view.ref = state.view.visit; sendView(); });
  $('ref-auto').addEventListener('click', () => { state.view.ref = -1; sendView(); });

  $('fov').addEventListener('input', (e) => {
    $('fov-value').textContent = e.target.value + t('unit.arcmin');
    renderFovCost();
  });
  $('fov').addEventListener('change', (e) => {
    state.fov = parseInt(e.target.value, 10);
    if (state.request) open(state.request, { autoplay: state.playing });
    renderTitle();
  });

  $('zoom-in').addEventListener('click', () => engine()?.zoom(1.4));
  $('zoom-out').addEventListener('click', () => engine()?.zoom(1 / 1.4));
  $('zoom-fit').addEventListener('click', () => engine()?.fit());

  $('sel-analyze').addEventListener('click', () => {
    setLevel('analyze');
    $('h-spectrum').closest('.panel').scrollIntoView({ block: 'start', behavior: reduceMotion ? 'auto' : 'smooth' });
  });
  $('sel-clear').addEventListener('click', () => engine()?.clearSelection());

  $('share').addEventListener('click', () => copy(shareURL(), t('share.copied')).then(() => {
    toast(t('share.copied'), {
      label: '</> embed',
      run: () => copy(`<iframe src="${shareURL({ embed: true })}" width="640" height="480" style="border:0" allow="clipboard-write"></iframe>`, t('share.embedCopied')),
    });
  }));
  $('lang').addEventListener('click', () => {
    setLang(getLang() === 'es' ? 'en' : 'es');
    $('lang').textContent = getLang() === 'es' ? 'EN' : 'ES';
    $('lang').setAttribute('aria-label', getLang() === 'es' ? 'English' : 'Español');
    renderCards();
    renderChannels();
    renderControls();
    renderFrame();
    renderTimeline();
    renderSelection();
    renderFovCost();
    renderBanner();
    renderArchiveStatus();
    writeURL();
  });
  $('help').addEventListener('click', () => $('help-dialog').showModal());
  $('archive-sim').addEventListener('click', () => {
    state.real = false;
    renderBanner();
    if (state.request) open(state.request, { autoplay: true });
  });

  const timeline = $('timeline');
  let scrubbing = false;
  timeline.addEventListener('pointerdown', (e) => {
    if (!state.field) return;
    scrubbing = true;
    timeline.setPointerCapture(e.pointerId);
    setPlaying(false);
    setVisit(visitAt(e.clientX));
  });
  timeline.addEventListener('pointermove', (e) => {
    if (scrubbing) {
      const v = visitAt(e.clientX);
      if (v !== state.view.visit) setVisit(v);
    }
  });
  timeline.addEventListener('pointerup', () => { scrubbing = false; });

  const viewer = $('viewer');
  new ResizeObserver(() => {
    engine()?.resize(Math.floor(viewer.clientWidth), Math.floor(viewer.clientHeight));
  }).observe(viewer);
  new ResizeObserver(() => {
    layoutTimeline();
    if (state.selection) renderSpectrum();
  }).observe($('timeline'));

  viewer.addEventListener('mousemove', (e) => {
    const r = viewer.getBoundingClientRect();
    const tip = $('tip');
    tip.style.left = `${e.clientX - r.left + 14}px`;
    tip.style.top = `${e.clientY - r.top + 14}px`;
  });
  const canvas = $('canvas');
  canvas.addEventListener('pointerenter', () => engine()?.pointer(true));
  canvas.addEventListener('pointermove', () => engine()?.pointer(true), { once: true });
  canvas.addEventListener('pointerleave', () => {
    engine()?.pointer(false);
    $('tip').hidden = true;
  });

  document.addEventListener('keydown', onKey);
}

function onKey(e) {
  const tag = e.target.tagName;
  if (e.target.closest?.('dialog') || ['INPUT', 'SELECT', 'TEXTAREA'].includes(tag)) {
    if (e.key === 'Escape' && e.target.id === 'q') e.target.blur();
    return;
  }
  if (e.ctrlKey || e.metaKey || e.altKey) return;
  if (tag === 'BUTTON' && (e.key === ' ' || e.key === 'Enter')) return;

  const f = state.field;
  const k = e.key;
  let handled = true;
  if (k === '/') $('q').focus();
  else if (k === '?') $('help-dialog').showModal();
  else if (!f) handled = false;
  else if (k === ' ') setPlaying(!state.playing);
  else if (k === 'ArrowRight' || k === 'ArrowLeft') {
    setPlaying(false);
    setVisit(nextVisit(k === 'ArrowRight' ? 1 : -1, e.shiftKey ? 'season' : 'visit'));
  } else if (k === 'PageUp' || k === 'PageDown') {
    setPlaying(false);
    setVisit(nextVisit(k === 'PageDown' ? 1 : -1, 'season'));
  } else if (k === 'Home') setVisit(0);
  else if (k === 'End') setVisit(f.visits.length - 1);
  else if (k >= '1' && k <= '6') setBand(parseInt(k, 10) - 1);
  else if (k === 'e' || k === 'E') setMode('epoch');
  else if (k === 'd' || k === 'D') setMode('diff');
  else if (k === 's' || k === 'S') setMode('sum');
  else if (k === 'a' || k === 'A') { state.view.ref = state.view.visit; sendView(); }
  else if (k === 'c' || k === 'C') {
    if (state.level === 'explore') setLevel('compare');
    $('catalog').checked = state.view.catalog = !state.view.catalog;
    sendView();
  } else if (k === 'f' || k === 'F') {
    if (state.level !== 'analyze') setLevel('analyze');
    $('flags').checked = state.view.flags = !state.view.flags;
    sendView();
  } else if (k === '+' || k === '=') engine()?.zoom(1.4);
  else if (k === '-') engine()?.zoom(1 / 1.4);
  else if (k === '0') engine()?.fit();
  else if (k === '[' || k === ']') {
    const speeds = [0.5, 1, 2, 4];
    const i = clamp(speeds.indexOf(state.speed) + (k === ']' ? 1 : -1), 0, speeds.length - 1);
    state.speed = speeds[i];
    $('speed').value = String(state.speed);
  } else if (k === 'Escape') engine()?.clearSelection();
  else handled = false;
  if (handled) e.preventDefault();
}

// ---------- Boot ----------

const params = new URLSearchParams(location.search);
if (params.has('debug')) globalThis.skyState = state;
setLang(params.get('lang') || detectLang());
$('lang').textContent = getLang() === 'es' ? 'EN' : 'ES';
renderChannels();
renderControls();
renderFovCost();
wire();
const archiveAvailable = detectArchive();
archiveAvailable.then((real) => {
  state.real = real;
  renderBanner();
});
if (!['ra', 'target', 'embed'].some((k) => params.has(k))) $('welcome').hidden = false;
run({ wasm: 'app.wasm' }).catch((err) => {
  console.error(err);
  toast(String(err));
});
