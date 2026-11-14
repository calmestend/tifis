"""Simulación de SPHEREx: el calendario de visitas por temporadas, las bandas, los píxeles, las
banderas y los movimientos imitan los datos reales, así que todo lo que viene después
(composición, fotometría, seguimiento) es común."""
import hashlib
from functools import lru_cache

import astropy.units as u
import numpy as np
from astropy.coordinates import BarycentricMeanEcliptic, SkyCoord
from scipy.special import erf

from science import (CHANNELS, HOT, MISSING, MJD_J2000, NONLINEAR, PIX, SUB_BANDS, TRANSIENT,
                     Frame, find_target, grid, npix, position, target_position)

SURVEY_START = 60796  # 2025-05-01
PROCESSING_DAYS = 7  # lo último que publica el archivo tiene una semana
STAR_BOX = 13  # minutos de arco de cielo poblado alrededor del campo
SATURATION = 800  # en unidades de la imagen; por encima el píxel queda NONLINEAR
GAIA_MJD = 57388.5
# Por canal: ruido por píxel y fondo típico en unidades de la imagen, FWHM en píxeles.
NOISE = [0.004, 0.004, 0.004, 0.005, 0.008, 0.010]
ZODI = [0.12, 0.10, 0.07, 0.05, 0.09, 0.14]
FWHM = [1.7, 1.7, 1.8, 1.9, 2.0, 2.1]
# Bandas de absorción (centro µm, anchura µm, profundidad).
BANDS = {
    'brown': [(1.15, 0.05, 0.5), (1.4, 0.08, 0.75), (1.65, 0.04, 0.45), (1.9, 0.10, 0.75),
              (2.25, 0.12, 0.5), (2.7, 0.20, 0.8), (3.3, 0.15, 0.85), (4.6, 0.12, 0.35)],
    'mira': [(1.4, 0.08, 0.4), (1.9, 0.10, 0.4), (2.3, 0.05, 0.3), (2.7, 0.20, 0.5), (4.6, 0.10, 0.35)],
    'young': [(3.05, 0.15, 0.25), (4.67, 0.06, 0.2)],
}


def seed(text):
    return int.from_bytes(hashlib.sha256(text.encode()).digest()[:8], 'little')


def flux_from_mag(mag):
    return 10 * 10 ** (-0.4 * (np.asarray(mag) - 10))


def planck(lam, temp):
    """B_ν salvo una constante, λ en µm."""
    x = 14387.77 / (lam * np.asarray(temp))
    return np.where(x > 700, 0, 1 / (lam ** 3 * np.expm1(np.minimum(x, 700))))


def shape(t, lam):
    """Forma del espectro del objeto t."""
    if t['sed'] == 'young':
        f = planck(lam, t['temp']) / planck(2.2, t['temp']) + 1.2 * planck(lam, 1100) / planck(2.2, 1100)
    else:
        f = planck(lam, t['temp'])
    for c, w, d in BANDS.get(t['sed'], []):
        f = f * (1 - d * np.exp(-0.5 * ((lam - c) / w) ** 2))
    return f


def dmag(t, lam, mjd):
    """Cambio de magnitud en mjd; positivo es más débil."""
    if t['sed'] == 'mira':
        return -t['amp'] * (1.2 / lam) ** 0.6 * np.cos(2 * np.pi * (mjd - t['phase']) / t['period'])
    if t['sed'] == 'young':
        return t['amp'] * (1.2 / lam) ** 0.8 * irregular(mjd, seed(t['id']))
    return 0.0


def irregular(mjd, s):
    """Oscilación suave y determinista en [-1, 1.5] con bajones ocasionales, como los episodios
    de oscurecimiento de las estrellas jóvenes."""
    phases = np.random.default_rng(s).random(5) * 2 * np.pi
    v = sum(0.25 * np.sin(2 * np.pi * mjd / p + ph) for p, ph in zip([3.1, 11.7, 37.0, 142.0], phases))
    dip = np.sin(2 * np.pi * mjd / 410 + phases[4])
    return float(np.clip(v + 2.5 * max(dip - 0.6, 0), -1, 1.5))


def flux_at(t, lam, mjd):
    return float(flux_from_mag(t['mag']) * shape(t, lam) / shape(t, t['ref']) * 10 ** (-0.4 * dmag(t, lam, mjd)))


def sun_longitude(mjd):
    """Longitud eclíptica media del Sol, con unos 2° de error."""
    return (280.460 + 0.9856474 * (mjd - MJD_J2000)) % 360


def schedule(lon, lat, now, r):
    """Calendario de visitas según la geometría del survey: un punto se observa cuando el Sol está
    a unos 90° en longitud eclíptica, dos veces al año; cerca de los polos eclípticos casi sin
    parar. Devuelve (MJD, temporada, sub-banda) por visita."""
    s = abs(np.sin(np.radians(lat)))
    per_season = 17 + round(7 * s ** 4)
    window = 150 if abs(lat) > 78 else 2 + 10 * s * s
    end = now - PROCESSING_DAYS
    visits, season, t = [], 0, SURVEY_START - 200.0
    while t < end + window:
        t += (180 - (sun_longitude(t) - lon - 90 + 720) % 180) / 0.9856474
        lo, hi = t - window / 2, t + window / 2
        t += 1
        if hi < SURVEY_START or lo > end:
            continue
        perm = r.permutation(SUB_BANDS)
        added = 0
        for j in range(per_season):
            m = lo + window * (j + 0.2 + 0.6 * r.random()) / per_season
            sub = float(np.clip(perm[j % SUB_BANDS] + 0.6 * r.random() - 0.3, 0, SUB_BANDS - 1))
            if SURVEY_START <= m <= end:
                visits.append((m, season, sub))
                added += 1
        if added:
            season += 1
    return sorted(visits)


@lru_cache(maxsize=16)
def field(ra, dec, fov, now):
    """Planifica un campo simulado en el día now: qué visitas lo cubren, a qué longitudes de onda y
    qué hay en el cielo. Devuelve el plan (lo que recibe el motor) y el cielo; los píxeles los genera
    frame(). Depende solo de sus argumentos, así que el motor puede pedir cada imagen después."""
    sd = seed(f'{ra:.4f} {dec:.4f}')
    r = np.random.default_rng(sd)
    c = SkyCoord(ra * u.deg, dec * u.deg)
    ecl = c.transform_to(BarycentricMeanEcliptic())
    visits = schedule(ecl.lon.deg, ecl.lat.deg, now, r)
    available = r.random((6, len(visits))) > 0.04
    n = npix(fov)
    t = find_target(ra, dec, fov, visits[len(visits) // 2][0] if visits else now)

    # Estrellas de campo, más densas cerca del plano de la Vía Láctea (por arcmin², hasta K≈17.5).
    count = int((1.2 + 6 * np.exp(-abs(c.galactic.b.deg) / 12)) * STAR_BOX ** 2)
    half = STAR_BOX * 60 / PIX / 2
    x, y = n / 2 + (r.random(count) * 2 - 1) * half, n / 2 + (r.random(count) * 2 - 1) * half
    sra, sdec = grid(ra, dec, n).all_pix2world(x - 0.5, y - 0.5, 0)
    mag = np.maximum(6, 17.5 + np.log10(1 - r.random(count)) / 0.32)
    stars = dict(ra=sra, dec=sdec, pmra=r.normal(0, 6, count), pmdec=r.normal(0, 6, count),
                 temp=2800 + 7000 * r.random(count) ** 2, mag=mag)

    plan = dict(real=False, ra=ra, dec=dec, npix=n, target=t['id'] if t else '', name=t['name'] if t else '',
                fluxUnit='', now=now, visits=[dict(mjd=m, season=s, sub=sub) for m, s, sub in visits],
                files=[[f'level2_sim_{v:04d}D{ch + 1}_spx.fits' if available[ch, v] else ''
                        for v in range(len(visits))] for ch in range(6)])
    return plan, dict(stars=stars, target=t, seed=sd, lon=ecl.lon.deg, lat=ecl.lat.deg)


def catalog(ra, dec, fov, now):
    """La capa de catálogo de la simulación: las estrellas con K < 16 en la época de Gaia y, primero,
    el objeto de prueba si está en el campo. Devuelve (fuentes, índice del objeto)."""
    _, sky = field(ra, dec, fov, now)
    s = sky['stars']
    sra, sdec = position(s['ra'], s['dec'], s['pmra'], s['pmdec'], MJD_J2000, GAIA_MJD)
    out = [dict(catalog='Gaia DR3', name='', ra=a, dec=d, pmra=pa, pmdec=pd, mjd=GAIA_MJD, mag=m)
           for a, d, pa, pd, m, k in zip(sra.tolist(), sdec.tolist(), s['pmra'].tolist(), s['pmdec'].tolist(),
                                         s['mag'].tolist(), (s['mag'] < 16).tolist()) if k]
    t = sky['target']
    if t is None:
        return out, -1
    tra, tdec = target_position(t, GAIA_MJD)
    return [dict(catalog='Gaia DR3', name=t['name'], ra=tra, dec=tdec, pmra=t['pmra'], pmdec=t['pmdec'],
                 mjd=GAIA_MJD, mag=t['mag'])] + out, 0


@lru_cache(maxsize=3000)
def frame(ra, dec, fov, now, c, v):
    """Los píxeles de la imagen del canal c en la visita v."""
    plan, sky = field(ra, dec, fov, now)
    vis = plan['visits'][v]
    mjd = vis['mjd']
    lo, hi = CHANNELS[c]
    lam = lo + (vis['sub'] + 0.5) / SUB_BANDS * (hi - lo)
    r = np.random.default_rng([sky['seed'], c, v])
    n = plan['npix']

    # Fondo zodiacal con un poco de pendiente, que cambia con la época y la latitud eclíptica.
    bg = ZODI[c] * (1 + 0.15 * np.sin(2 * np.pi * mjd / 365.25 + np.radians(sky['lon']))) \
        * (1 + 0.3 * (1 - abs(np.sin(np.radians(sky['lat'])))))
    gx, gy = r.normal(0, bg * 0.001, 2)
    j, i = np.indices((n, n))
    img = bg + gx * (i - n / 2) + gy * (j - n / 2)

    s = sky['stars']
    sra, sdec = position(s['ra'], s['dec'], s['pmra'], s['pmdec'], MJD_J2000, mjd)
    flux = flux_from_mag(s['mag']) * planck(lam, s['temp']) / planck(2.2, s['temp'])
    t = sky['target']
    if t is not None:
        tra, tdec = target_position(t, mjd)
        sra, sdec, flux = np.append(sra, tra), np.append(sdec, tdec), np.append(flux, flux_at(t, lam, mjd))
    x, y = grid(plan['ra'], plan['dec'], n).all_world2pix(sra, sdec, 0)
    x, y = x + 0.5, y + 0.5
    near = (x > -6) & (y > -6) & (x < n + 6) & (y < n + 6) & (flux > 1e-5)
    add_psf(img, x[near], y[near], FWHM[c] / 2.3548, flux[near])

    flags = np.zeros((n, n), np.uint32)
    sat = img > SATURATION
    img[sat] = SATURATION
    flags[sat] |= NONLINEAR
    img += r.normal(0, NOISE[c], (n, n))

    hot = r.integers(n * n, size=r.integers(n * n // 4000 + 2))
    img.flat[hot] += 0.05 + 0.4 * r.random(hot.size)
    flags.flat[hot] |= HOT
    for _ in range(r.integers(n * n // 1500 + 2)):  # rayos cósmicos cortos
        x0, y0 = r.random(2) * n
        a, e = r.random() * 2 * np.pi, 0.3 + 3 * r.random()
        for k in range(1 + r.integers(4)):
            px, py = int(x0 + k * np.cos(a)), int(y0 + k * np.sin(a))
            if 0 <= px < n and 0 <= py < n:
                img[py, px] += e
                flags[py, px] |= TRANSIENT
    if r.random() < 0.05:  # a veces falta una franja en un borde
        w = 2 + r.integers(4)
        cols = slice(0, w) if r.integers(2) == 0 else slice(n - w, n)
        img[:, cols] = 0
        flags[:, cols] |= MISSING
    return Frame(img.astype(np.float32), flags, np.full((n, n), lam, np.float32), None, mjd, vis['sub'])


def add_psf(img, x, y, sigma, flux):
    """Suma gaussianas integradas sobre cada píxel (con erf), para que los desplazamientos de menos
    de un píxel se vean bien. x, y en la convención del motor (el píxel i cubre [i, i+1))."""
    n = len(img)
    rad = int(np.ceil(4 * sigma)) + 1
    size = 2 * rad + 1
    k = 1 / (np.sqrt(2) * sigma)
    x0, y0 = np.floor(x).astype(int) - rad, np.floor(y).astype(int) - rad
    edges = np.arange(size + 1)
    ex = 0.5 * np.diff(erf((x0[:, None] + edges - x[:, None]) * k), axis=1)
    ey = 0.5 * np.diff(erf((y0[:, None] + edges - y[:, None]) * k), axis=1)
    px = np.broadcast_to((x0[:, None] + np.arange(size))[:, None, :], (len(x), size, size))
    py = np.broadcast_to((y0[:, None] + np.arange(size))[:, :, None], (len(x), size, size))
    patch = flux[:, None, None] * ey[:, :, None] * ex[:, None, :]
    ok = (px >= 0) & (py >= 0) & (px < n) & (py < n)
    np.add.at(img, (py[ok], px[ok]), patch[ok])
