"""La parte científica: rejilla del campo, reproyección de los recortes de SPHEREx,
fotometría, seguimiento de fuentes y los objetos de prueba del reto.

Coordenadas de píxel: hacia fuera (API y motor) el píxel i cubre [i, i+1), como en el
visor; astropy y photutils ponen el centro del píxel i en i. Por dentro se resta 0.5.
"""
import warnings
from bisect import bisect_left
from dataclasses import dataclass
from functools import cached_property
from io import BytesIO

import astropy.units as u
import numpy as np
from astropy import log
from astropy.coordinates import angular_separation
from astropy.io import fits
from astropy.stats import SigmaClip, mad_std
from astropy.time import Time
from astropy.utils.exceptions import AstropyWarning
from astropy.wcs import WCS
from photutils.aperture import ApertureStats, CircularAnnulus, CircularAperture

warnings.simplefilter('ignore', AstropyWarning)
log.setLevel('WARNING')

PIX = 6.15  # segundos de arco por píxel de la rejilla (el de SPHEREx)
SUB_BANDS = 17  # canales espectrales por detector: filtros lineales variables
CHANNELS = [(0.75, 1.10), (1.10, 1.60), (1.60, 2.40), (2.40, 3.80), (3.80, 4.40), (4.40, 5.00)]  # µm
SEASON_GAP = 40  # días: las pasadas del survey van separadas unos seis meses
MJD_J2000 = 51544.5
# Suma de MJy/sr en la apertura × ángulo sólido del píxel (sr) = MJy; × 1e9 = mJy.
FLUX_SCALE = (PIX * u.arcsec).to_value(u.rad) ** 2 * 1e9

# Bits de la extensión FLAGS (palabras clave MSKNnnnn de cada imagen).
FLAG_BITS = {
    'TRANSIENT': 0, 'OVERFLOW': 1, 'SUR_ERROR': 2, 'NONFUNC': 6, 'DICHROIC': 7,
    'MISSING_DATA': 9, 'HOT': 10, 'COLD': 11, 'FULLSAMPLE': 12, 'PHANMISS': 14,
    'NONLINEAR': 15, 'PERSIST': 17, 'OUTLIER': 19, 'CROSSTALK': 20, 'SOURCE': 21,
    'GHOST': 22, 'GHOST_FPA': 23, 'GHOST_EXT': 24, 'STREAK': 25, 'BLOOM': 26,
    'SNOWBALL': 27, 'HALO': 28,
}
# Los que invalidan un píxel para medir: la lista de la Source Discovery Tool para su aviso
# CONTAINS_BAD_PIXEL. SOURCE, FULLSAMPLE, PHANMISS y OUTLIER son informativos.
BAD = sum(1 << FLAG_BITS[n] for n in (
    'TRANSIENT', 'OVERFLOW', 'SUR_ERROR', 'NONFUNC', 'DICHROIC', 'MISSING_DATA', 'HOT', 'COLD',
    'NONLINEAR', 'CROSSTALK', 'GHOST', 'GHOST_FPA', 'GHOST_EXT', 'STREAK', 'PERSIST', 'BLOOM',
    'SNOWBALL', 'HALO'))
TRANSIENT, MISSING, HOT, NONLINEAR = 1 << 0, 1 << 9, 1 << 10, 1 << 15

# Radios de apertura y anillo en píxeles, como la Source Discovery Tool. El del centroide es
# más ceñido: ahí importa más la rapidez que evitar las alas de la PSF.
APERTURE, ANNULUS, CENTROID_ANNULUS = 3.0, (7.0, 16.0), (4.5, 7.5)
DETECTION_SNR = 5
CLIP = SigmaClip(sigma=3, maxiters=5)  # centro en la mediana, como la Source Discovery Tool

# Objetos de prueba del reto: posición J2000 en la época J2000 y movimiento propio de Simbad
# (Gaia DR3; CatWISE para WISE 0855). sed, temp, mag, ref, period, amp y phase solo los usa
# la simulación (mag en la banda K, ref es su longitud de onda en µm).
TARGETS = [
    dict(id='barnard', name="Barnard's Star", band=1, mode='diff',
         aliases=['barnard', 'estrella de barnard', 'gj 699', "barnard's star"],
         ra=269.45207696, dec=4.69336497, pmra=-801.551, pmdec=10362.394,
         sed='star', temp=3200, mag=4.52, ref=2.2),
    dict(id='eps-indi-b', name='Epsilon Indi B', band=2, mode='diff',
         aliases=['eps ind b', 'epsilon indi b', 'eps indi b', 'ε indi b', 'epsilon indi ba'],
         ra=331.04414659, dec=-56.78284420, pmra=3981.977, pmdec=-2466.832,
         sed='brown', temp=1100, mag=11.21, ref=2.2),
    dict(id='wise0855', name='WISE 0855-0714', band=5, mode='diff',
         aliases=['wise 0855', 'wise 0855-0714', 'wise j0855', 'wisea j085510.74-071442.5',
                  'wise j085510.83-071442.5'],
         ra=133.81890825, dec=-7.24704800, pmra=-8123.7, pmdec=673.2,
         sed='brown', temp=285, mag=13.9, ref=4.6),
    dict(id='rw-aur', name='RW Aurigae', band=3, mode='epoch', aliases=['rw aur', 'rw aurigae'],
         ra=76.95652603, dec=30.40143817, pmra=7.23, pmdec=-20.33,
         sed='young', temp=4500, mag=7.02, ref=2.2, amp=0.7),
    # K es 2.2 en realidad; la simulación la baja 2 mag para que el núcleo solo se sature cerca
    # del máximo y la pulsación se siga viendo.
    dict(id='r-dra', name='R Draconis', band=2, mode='epoch', aliases=['r dra', 'r draconis'],
         ra=248.16761083, dec=66.75497942, pmra=-20.643, pmdec=6.149,
         sed='mira', temp=2600, mag=4.2, ref=2.2, period=245.6, amp=0.9, phase=60950),
]


def now_mjd():
    return Time.now().mjd


def position(ra, dec, pmra, pmdec, ref_mjd, mjd):
    """Posición en mjd con movimiento propio lineal (mas/año; pmra incluye cos δ)."""
    yr = (mjd - ref_mjd) / 365.25
    return ra + pmra * yr / 3.6e6 / np.cos(np.radians(dec)), dec + pmdec * yr / 3.6e6


def target_position(t, mjd):
    return position(t['ra'], t['dec'], t['pmra'], t['pmdec'], MJD_J2000, mjd)


def separation(ra1, dec1, ra2, dec2):
    """Distancia angular en grados."""
    return angular_separation(ra1 * u.deg, dec1 * u.deg, ra2 * u.deg, dec2 * u.deg).to_value(u.deg)


def targets_now():
    now = now_mjd()
    out = []
    for t in TARGETS:
        ra, dec = target_position(t, now)
        out.append({k: t[k] for k in ('id', 'name', 'aliases', 'pmra', 'pmdec', 'band', 'mode')} | dict(ra=ra, dec=dec))
    return out


def find_target(ra, dec, fov, mjd):
    """El objeto de prueba que cae en un campo de fov minutos de arco, si hay alguno."""
    return next((t for t in TARGETS if separation(ra, dec, *target_position(t, mjd)) < fov / 60), None)


def find_in_catalog(sources, t):
    """Índice de la fuente de catálogo que es el objeto t: la más cercana, a menos de 5″, a donde
    lo pone su movimiento propio en la época del catálogo. -1 si no está."""
    if t is None or not sources:
        return -1
    ra, dec, mjd = (np.array([s[k] for s in sources]) for k in ('ra', 'dec', 'mjd'))
    d = separation(ra, dec, *target_position(t, mjd)) * 3600
    k = int(np.argmin(d))
    return k if d[k] < 5 else -1


def npix(fov):
    return max(16, round(fov * 60 / PIX))


def cutout_size(fov):
    """Lado del recorte en grados. Llega en la orientación del detector, así que debe cubrir las
    esquinas del campo norte-arriba en cualquier ángulo."""
    return fov / 60 * np.sqrt(2) * 1.02


def grid(ra, dec, n):
    """La rejilla del campo: TAN centrada en (ra, dec), norte arriba, este a la izquierda."""
    w = WCS(naxis=2)
    w.wcs.ctype = ['RA---TAN', 'DEC--TAN']
    w.wcs.crval = [ra, dec]
    w.wcs.cdelt = [-PIX / 3600, -PIX / 3600]
    w.wcs.crpix = [n / 2 + 0.5, n / 2 + 0.5]  # el punto tangente, en n/2 para el motor
    return w


def plan(visits, ra, dec, fov, now):
    """Organiza las imágenes de un campo en visitas, una por exposición. En cada exposición el punto
    cae en una pareja de detectores (D1+D4, D2+D5 o D3+D6), así que los otros cuatro canales no
    tienen imagen en ella. La sub-banda se sabe al leer la primera imagen de la visita."""
    exposures = {}
    for im in visits:
        if 1 <= im['detector'] <= 6:
            e = exposures.setdefault(im['exposure'], {'mjd': im['mjd'], 'files': [''] * 6})
            e['mjd'] = min(e['mjd'], im['mjd'])
            e['files'][im['detector'] - 1] = im['path']
    exps = sorted(exposures.values(), key=lambda e: e['mjd'])
    mjds = [e['mjd'] for e in exps]
    t = find_target(ra, dec, fov, now)
    return dict(real=True, ra=ra, dec=dec, npix=npix(fov), target=t['id'] if t else '',
                name=t['name'] if t else '', fluxUnit='mJy',
                visits=[dict(mjd=m, season=s, sub=-1) for m, s in zip(mjds, seasons(mjds))],
                files=[[e['files'][c] for e in exps] for c in range(6)])


def seasons(mjds):
    """Número de temporada de cada visita: más de 40 días sin visitas empiezan una nueva."""
    return np.concatenate([[0], np.cumsum(np.diff(mjds) > SEASON_GAP)]).astype(int).tolist() if mjds else []


@dataclass
class Frame:
    """Una imagen en la rejilla del campo."""
    image: np.ndarray  # MJy/sr con la luz zodiacal restada (unidades propias en la simulación)
    flags: np.ndarray  # uint32, bits de FLAGS
    lam: np.ndarray  # µm que ve cada píxel
    variance: np.ndarray | None  # (MJy/sr)²; la simulación no tiene
    mjd: float
    sub: float  # sub-banda de la visita, 0–16

    def __post_init__(self):
        self.bg, self.sigma = stats(self.image, self.flags)

    @cached_property
    def bad(self):
        return (self.flags & BAD) != 0

    def dump(self):
        buf = BytesIO()
        np.savez(buf, image=self.image, flags=self.flags, lam=self.lam, variance=self.variance,
                 mjd=self.mjd, sub=self.sub)
        return buf.getvalue()

    @classmethod
    def load(cls, data):
        z = np.load(BytesIO(data))
        return cls(z['image'], z['flags'], z['lam'], z['variance'], float(z['mjd']), float(z['sub']))

    def payload(self):
        """Lo que recibe el motor, todo little-endian: cinco float64 (MJD, λ en el centro, fondo, σ,
        sub-banda) seguidos de la imagen (float32) y las banderas (uint32), N×N cada una."""
        n = len(self.image)
        head = np.array([self.mjd, self.lam[n // 2, n // 2], self.bg, self.sigma, self.sub], '<f8')
        return head.tobytes() + self.image.astype('<f4').tobytes() + self.flags.astype('<u4').tobytes()


def process(raw, ra, dec, n, channel):
    """Lleva un recorte de IRSA a la rejilla del campo. Llega en la orientación del detector y con
    distorsión SIP, así que cada píxel de la rejilla se busca con el WCS de la imagen; se resta el
    modelo ZODI para que el fondo coincida entre visitas y las banderas se toman del píxel más
    cercano. Lo que cae fuera del recorte queda como MISSING_DATA."""
    with fits.open(BytesIO(raw)) as hdul:
        h = hdul['IMAGE'].header
        image = hdul['IMAGE'].data.astype(float) - hdul['ZODI'].data
        variance = hdul['VARIANCE'].data.astype(float)
        flags = hdul['FLAGS'].data.astype(np.uint32)
        cel = WCS(h)
        # La longitud de onda sale de la tabla WAVE-TAB, sin la distorsión (tutorial spherex_intro).
        spec = WCS(h, fobj=hdul, key='W')
        spec.sip = None
        j, i = np.indices((n, n))
        x, y = cel.all_world2pix(*grid(ra, dec, n).all_pix2world(i, j, 0), 0, quiet=True)
        x, y = np.where(np.isfinite(x), x, -10), np.where(np.isfinite(y), y, -10)
        lam = np.nan_to_num(spec.pixel_to_world(x, y)[0].to_value(u.um))
        mjd = float(h['MJD-AVG'])

    xi, yi = np.rint(x).astype(int), np.rint(y).astype(int)
    inside = (xi >= 0) & (yi >= 0) & (xi < image.shape[1]) & (yi < image.shape[0])
    fl = np.full((n, n), MISSING, np.uint32)
    fl[inside] = flags[yi[inside], xi[inside]]
    img, ok = bilinear(image, x, y)
    var, vok = bilinear(variance, x, y)
    ok &= inside
    fl[~ok] |= MISSING
    lo, hi = CHANNELS[channel]
    sub = float(np.clip((lam[n // 2, n // 2] - lo) / (hi - lo) * SUB_BANDS - 0.5, 0, SUB_BANDS - 1))
    return Frame(np.where(ok, img, 0).astype(np.float32), fl, lam.astype(np.float32),
                 np.where(ok & vok, var, 0).astype(np.float32), mjd, sub)


def bilinear(a, x, y):
    """Interpola a en (x, y) de base 0 saltando los píxeles no finitos y repartiendo su peso entre
    los demás. ok es falso donde queda menos de un cuarto del peso."""
    h, w = a.shape
    x0, y0 = np.floor(x).astype(int), np.floor(y).astype(int)
    total, weight = np.zeros(x.shape), np.zeros(x.shape)
    for dy in (0, 1):
        for dx in (0, 1):
            xi, yi = x0 + dx, y0 + dy
            v = np.where((xi >= 0) & (yi >= 0) & (xi < w) & (yi < h),
                         a[np.clip(yi, 0, h - 1), np.clip(xi, 0, w - 1)], np.nan)
            wt = np.where(np.isfinite(v), (1 - np.abs(dx - (x - x0))) * (1 - np.abs(dy - (y - y0))), 0)
            total += wt * np.nan_to_num(v)
            weight += wt
    ok = weight >= 0.25
    return np.where(ok, total / np.where(ok, weight, 1), 0), ok


def stats(image, flags):
    """Mediana y σ robusta (MAD escalada) de los píxeles sin banderas malas."""
    v = image[(flags & BAD) == 0]
    if v.size == 0:
        return 0.0, 1.0
    return float(np.median(v)), max(float(mad_std(v)), 1e-6)


def background(fr, x, y, rin, rout):
    """Mediana con recorte a 3σ de los píxeles buenos de un anillo, como la Source Discovery Tool."""
    m = ApertureStats(fr.image, CircularAnnulus((x - 0.5, y - 0.5), rin, rout), mask=fr.bad,
                      sigma_clip=CLIP).median
    return float(m) if np.isfinite(m) else fr.bg


def aperture(fr, x, y):
    """Suma sobre el fondo local dentro de APERTURE. Los píxeles malos no cuentan, salvo los
    saturados, que aún llevan señal. El error sale de VARIANCE cuando la hay. Devuelve flujo,
    error, píxeles buenos y píxeles de la apertura."""
    bg = background(fr, x, y, *ANNULUS)
    m = CircularAperture((x - 0.5, y - 0.5), APERTURE).to_mask('center')
    skip = (fr.flags & (BAD & ~NONLINEAR)) != 0
    vals = m.get_values(fr.image, mask=skip) - bg
    npix = int(m.data.sum())
    if fr.variance is not None:
        err = np.sqrt(m.get_values(fr.variance, mask=skip).sum())
    else:
        nann = np.pi * (ANNULUS[1] ** 2 - ANNULUS[0] ** 2)
        err = fr.sigma * np.sqrt(npix + np.pi / 2 * npix ** 2 / nann)
    return float(vals.sum()), float(err), int(vals.size), npix


def centroid(fr, x, y, max_shift):
    """Afina una posición con la media ponderada por flujo dentro de 2 px, sin moverse más de
    max_shift. snr dice cuán claramente hay una fuente ahí."""
    bg = background(fr, x, y, *CENTROID_ANNULUS)
    n = len(fr.image)
    cx, cy, snr = x, y, 0.0
    for _ in range(4):
        j, i = np.mgrid[int(cy) - 2:int(cy) + 3, int(cx) - 2:int(cx) + 3]
        keep = (i >= 0) & (j >= 0) & (i < n) & (j < n) & (np.hypot(i + 0.5 - cx, j + 0.5 - cy) <= 2)
        i, j = i[keep], j[keep]
        # Las estrellas brillantes se saturan y quedan marcadas, pero esos píxeles siguen diciendo
        # dónde está la estrella: solo se saltan los que no tienen datos.
        ok = (fr.flags[j, i] & MISSING) == 0
        i, j = i[ok], j[ok]
        v = fr.image[j, i] - bg
        w = np.clip(v, 0, None)
        if v.size == 0 or w.sum() == 0:
            return x, y, 0.0
        snr = v.sum() / (fr.sigma * np.sqrt(v.size))
        nx, ny = (w * (i + 0.5)).sum() / w.sum(), (w * (j + 0.5)).sum() / w.sum()
        d = np.hypot(nx - x, ny - y)
        if d > max_shift:
            nx, ny = x + (nx - x) * max_shift / d, y + (ny - y) * max_shift / d
        cx, cy = nx, ny
    return float(cx), float(cy), float(snr)


def at(m, mjd):
    return m['x0'] + m['vx'] * (mjd - m['t0']), m['y0'] + m['vy'] * (mjd - m['t0'])


def track(frames, mjds, band, visit, x, y):
    """Sigue el centroide de una fuente por las imágenes cargadas de un canal, desde la visita donde
    se eligió hacia delante y hacia atrás, y ajusta su movimiento."""
    order = sorted(v for c, v in frames if c == band)
    start = bisect_left(order, visit)
    pts = []
    for seq in (order[start:], order[:start][::-1]):
        gx, gy = x, y
        for v in seq:
            cx, cy, snr = centroid(frames[band, v], gx, gy, 1.5)
            if snr >= DETECTION_SNR:
                gx, gy = cx, cy
                pts.append(dict(v=v, x=cx, y=cy))
    pts.sort(key=lambda p: p['v'])
    m = fit(pts, mjds, x, y)
    # Una pasada de rechazo de puntos discrepantes.
    kept = [p for p in pts if np.hypot(*np.subtract(at(m, mjds[p['v']]), (p['x'], p['y']))) < 0.6]
    if 2 <= len(kept) < len(pts):
        m = fit(kept, mjds, x, y) | dict(track=pts)
    return m


def fit(pts, mjds, x, y):
    """Ajuste lineal de la posición contra el tiempo, en píxeles y días, con los errores de la
    pendiente sacados de la dispersión alrededor del ajuste."""
    m = dict(x0=x, y0=y, t0=0.0, vx=0.0, vy=0.0, ex=0.0, ey=0.0, moving=False, track=pts)
    if not pts:
        return m
    t = np.array([mjds[p['v']] for p in pts])
    px, py = np.array([p['x'] for p in pts]), np.array([p['y'] for p in pts])
    m.update(t0=t.mean(), x0=px.mean(), y0=py.mean())
    if np.ptp(t) < 60:
        return m
    dt = t - t.mean()
    stt = (dt ** 2).sum()
    vx, vy = (dt * (px - px.mean())).sum() / stt, (dt * (py - py.mean())).sum() / stt
    if len(pts) > 2:
        m['ex'] = np.sqrt(((px - px.mean() - vx * dt) ** 2).sum() / (len(pts) - 2) / stt)
        m['ey'] = np.sqrt(((py - py.mean() - vy * dt) ** 2).sum() / (len(pts) - 2) / stt)
    # Cuenta como movimiento si es significativo y pasa de ~0.2 px/año; por debajo no se distingue
    # del ruido con este tamaño de píxel.
    speed = np.hypot(vx, vy)
    if speed * 365.25 > 0.2 and speed > 3 * np.hypot(m['ex'], m['ey']):
        m.update(vx=vx, vy=vy, moving=True)
    return m


def measure(frames, m, flux_scale):
    """Fotometría forzada a lo largo del movimiento ajustado en todas las imágenes cargadas de todos
    los canales: da a la vez el espectro y las curvas de luz."""
    out = []
    for (c, v), fr in sorted(frames.items()):
        x, y = at(m, fr.mjd)
        flux, err, good, npix = aperture(fr, x, y)
        if good == 0:  # nada usable en la apertura, como un núcleo saturado del todo
            continue
        n = len(fr.lam)
        lam = fr.lam[min(max(int(y), 0), n - 1), min(max(int(x), 0), n - 1)]
        out.append(dict(c=c, v=v, t=fr.mjd, l=float(lam), f=flux * flux_scale, e=err * flux_scale,
                        ok=bool(npix - good < 0.3 * npix)))
    return out


def select(frames, field, catalog, band, visit, x, y, flux_scale):
    """Todo lo que la interfaz muestra de una fuente elegida en (x, y) de la visita visit: posición,
    movimiento propio, trayectoria, medidas en cada imagen y la fuente de catálogo que coincide."""
    mjds = [v['mjd'] for v in field['visits']]
    if (fr := frames.get((band, visit))) is not None:
        cx, cy, snr = centroid(fr, x, y, 2)
        if snr >= DETECTION_SNR:
            x, y = cx, cy
    m = track(frames, mjds, band, visit, x, y)
    px, py = at(m, mjds[visit])
    g = grid(field['ra'], field['dec'], field['npix'])
    ra, dec = g.all_pix2world(px - 0.5, py - 0.5, 0)

    match = None
    if catalog:
        cra, cdec = position(*(np.array([s[k] for s in catalog]) for k in ('ra', 'dec', 'pmra', 'pmdec', 'mjd')),
                             mjds[visit])
        gx, gy = g.all_world2pix(cra, cdec, 0)
        d = np.hypot(gx + 0.5 - px, gy + 0.5 - py)
        k = int(np.argmin(d))
        if d[k] < 1.5:
            s = catalog[k]
            match = dict(name=s['name'] or 'Gaia DR3', pmra=s['pmra'] / 1000, pmdec=s['pmdec'] / 1000)

    yr = PIX * 365.25  # px/día → ″/año
    return dict(ra=float(ra), dec=float(dec), band=band, moving=m['moving'],
                pmra=-m['vx'] * yr, pmdec=-m['vy'] * yr, pmraErr=m['ex'] * yr, pmdecErr=m['ey'] * yr,
                x0=m['x0'], y0=m['y0'], t0=m['t0'], vx=m['vx'], vy=m['vy'],
                track=m['track'], measurements=measure(frames, m, flux_scale), catalog=match)
