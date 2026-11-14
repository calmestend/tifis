"""Acceso a los archivos: imágenes de SPHEREx y CatWISE2020 en IRSA, Gaia DR3 en el archivo de la
ESA, y la caché en disco que los guarda.

IRSA tarda de segundos a minutos y no envía cabeceras CORS, así que todo pasa por aquí. Cada tipo
de petición tiene sus turnos: lo lento (catálogos) no debe ocupar los de lo urgente (listas de
imágenes y recortes).
"""
import hashlib
import logging
import os
import re
import tempfile
import threading
import time
import urllib.request
from concurrent.futures import Future
from io import BytesIO
from types import SimpleNamespace

import astropy.units as u
import numpy as np
from astropy.coordinates import SkyCoord
from astropy.io import fits
from astroquery.gaia import Gaia
from astroquery.ipac.irsa import Irsa

from science import position

IRSA = 'https://irsa.ipac.caltech.edu'
USER_AGENT = 'spherex-sky-timeline/0.2 (+Space Apps 2026)'
CATALOG_RADIUS = 0.15  # grados: cubre las esquinas del campo más grande (12′)
GAIA_MJD = 57388.5  # J2016.0, época de las posiciones de Gaia DR3
CATWISE_MJD = 57170.0  # 2015.4, época de las posiciones de CatWISE2020

# Los tutoriales de IRSA recomiendan de 7 a 12 recortes en paralelo; main.py lo ajusta.
cutout_slots = threading.BoundedSemaphore(8)
list_slots = threading.BoundedSemaphore(2)  # SIA/TAP de imágenes: camino crítico de la app
catwise_slots = threading.BoundedSemaphore(2)  # TAP de CatWISE: minutos
# El cliente de Gaia de astroquery guarda el estado de la última respuesta: de una en una.
gaia_slots = threading.BoundedSemaphore(1)

log = logging.getLogger('archive')


class Cache:
    """Respuestas guardadas en disco en <sha256(clave)[:2]>/<sha256(clave)>. Si llegan a la vez
    varias peticiones con la misma clave, la consulta se hace una sola vez."""

    def __init__(self, root):
        self.root = root
        self.lock = threading.Lock()
        self.inflight = {}

    def path(self, key):
        h = hashlib.sha256(key.encode()).hexdigest()
        return os.path.join(self.root, h[:2], h)

    def lookup(self, key, ttl=0):
        """El valor guardado si tiene menos de ttl segundos (0: sin caducidad), o None."""
        p = self.path(key)
        try:
            if ttl and time.time() - os.path.getmtime(p) >= ttl:
                return None
            with open(p, 'rb') as f:
                return f.read()
        except OSError:
            return None

    def put(self, key, data):
        """Escritura atómica: una caída nunca deja medio archivo."""
        p = self.path(key)
        try:
            os.makedirs(os.path.dirname(p), exist_ok=True)
            fd, tmp = tempfile.mkstemp(dir=os.path.dirname(p), prefix='.tmp-')
            with os.fdopen(fd, 'wb') as f:
                f.write(data)
            os.replace(tmp, p)
        except OSError as e:
            log.warning('cache: %s', e)

    def get(self, key, ttl, fetch):
        """(valor, acierto): el guardado si sigue vigente; si no, fetch() y se guarda."""
        data = self.lookup(key, ttl)
        if data is not None:
            return data, True
        with self.lock:
            call = self.inflight.get(key)
            owner = call is None
            if owner:
                call = self.inflight[key] = SimpleNamespace(done=threading.Event(), data=None, err=None)
        if not owner:
            call.done.wait()
            if call.err:
                raise call.err
            return call.data, False
        try:
            call.data = fetch()
            self.put(key, call.data)
            return call.data, False
        except Exception as e:
            call.err = e
            raise
        finally:
            with self.lock:
                del self.inflight[key]
            call.done.set()


def in_background(fn):
    """Corre fn en un hilo que no frena el cierre del servidor y devuelve su Future."""
    fut = Future()

    def run():
        try:
            fut.set_result(fn())
        except Exception as e:
            fut.set_exception(e)

    threading.Thread(target=run, daemon=True).start()
    return fut


def retry(fn):
    """Tres intentos, con esperas de 1 y 2 s. Los errores 4xx no se repiten."""
    for attempt in range(3):
        try:
            return fn()
        except Exception as e:
            code = getattr(e, 'code', None) or getattr(getattr(e, 'response', None), 'status_code', None)
            if (isinstance(code, int) and code < 500) or attempt == 2:
                raise
            log.info('retrying after %s', e)
            time.sleep(attempt + 1)


def http_get(url, timeout):
    def once():
        req = urllib.request.Request(url, headers={'User-Agent': USER_AGENT})
        with urllib.request.urlopen(req, timeout=timeout) as r:
            return r.read()
    return retry(once)


# ---------- Imágenes de SPHEREx ----------

FILE_RE = re.compile(r'level2_(\w+?_\d+_\d+)D(\d)_spx')


def valid_path(p):
    """Solo archivos de SPHEREx, para que nadie use el servidor para pedir otra cosa a IRSA."""
    return (p.startswith('ibe/data/spherex/') and p.endswith('.fits') and '..' not in p
            and not any(c in p for c in '?#\\'))


def visits_from_rows(rows):
    """(ruta o URL, MJD) → una imagen por exposición y detector. Un archivo puede aparecer
    reprocesado (_retry) o en dos entregas (QR3 y DR1): se queda DR1 y, dentro, la última ruta."""
    best = {}
    for uri, mjd in rows:
        m = FILE_RE.search(str(uri))
        try:
            mjd = float(mjd)
        except (TypeError, ValueError):
            continue
        if not m or not np.isfinite(mjd):
            continue
        path = str(uri).lstrip('/')
        i = path.find('ibe/data/spherex/')
        if i > 0:
            path = path[i:]
        parts = path.removeprefix('ibe/data/spherex/').split('/')
        release = parts[0] if len(parts) > 1 else ''
        v = dict(path=path, mjd=mjd, detector=int(m[2]), exposure=m[1], release=release)
        rank = ('1' if release.startswith('dr') else '0') + path
        key = (m[1], m[2])
        if key not in best or rank > best[key][0]:
            best[key] = (rank, v)
    return sorted((v for _, v in best.values()), key=lambda v: (v['mjd'], v['detector']))


def visits_sia(ra, dec):
    """Imágenes que contienen la posición, por SIA v2: responde en segundos pero ve los datos
    nuevos con alrededor de un día de retraso."""
    with list_slots:
        t = retry(lambda: Irsa.query_sia(pos=(SkyCoord(ra, dec, unit='deg'), 0.0003 * u.deg),
                                         collection=['spherex_dr1', 'spherex_qr3']))
    return visits_from_rows(zip(t['access_url'], t['t_min']))


def visits_tap(ra, dec):
    """Lo mismo por TAP (la consulta del tutorial de recortes): tarda decenas de segundos pero ve
    lo ingerido en el último día, y es otra vía cuando SIA está caído."""
    q = (f"SELECT a.uri, p.time_bounds_lower FROM spherex.artifact a "
         f"JOIN spherex.plane p ON a.planeid = p.planeid "
         f"WHERE 1 = CONTAINS(POINT('ICRS', {ra:.6f}, {dec:.6f}), p.poly) ORDER BY p.time_bounds_lower")
    with list_slots:
        t = retry(lambda: Irsa.query_tap(q).to_table())
    return visits_from_rows(zip(t['uri'], t['time_bounds_lower']))


def cutout(path, ra, dec, size):
    """Recorte de size grados sin la extensión EPSF, un cubo de PSF que es el 97 % del archivo."""
    url = f'{IRSA}/{path}?center={ra:.6f},{dec:.6f}d&size={size:.5f}'
    with cutout_slots:
        raw = http_get(url, 90)
    with fits.open(BytesIO(raw)) as hdul:
        if 'EPSF' in hdul:
            del hdul['EPSF']
        out = BytesIO()
        hdul.writeto(out)
    return out.getvalue()


# ---------- Catálogos ----------

def column(t, name):
    """Columna como float, con los nulos a 0."""
    return np.ma.filled(np.ma.asarray(t[name], dtype=float), 0)


def gaia(ra, dec, radius):
    """Fuentes de Gaia DR3 con G < 19 en un cono. gaia_source_lite tiene las columnas que hacen
    falta y responde antes; la consulta asíncrona no tiene el límite de 2000 filas de la síncrona."""
    q = (f"SELECT source_id, ra, dec, pmra, pmdec, phot_g_mean_mag FROM gaiadr3.gaia_source_lite "
         f"WHERE 1 = CONTAINS(POINT(ra, dec), CIRCLE({ra:.6f}, {dec:.6f}, {radius:.5f})) "
         f"AND phot_g_mean_mag < 19")
    with gaia_slots:
        t = retry(lambda: Gaia.launch_job_async(q).get_results())
    return [dict(catalog='Gaia DR3', name=f'Gaia DR3 {sid}', ra=r, dec=d, pmra=pa, pmdec=pd, mjd=GAIA_MJD, mag=g)
            for sid, r, d, pa, pd, g in zip(t['source_id'].tolist(), *(column(t, c).tolist() for c in (
                'ra', 'dec', 'pmra', 'pmdec', 'phot_g_mean_mag')))]


def catwise(ra, dec, radius):
    """Fuentes de CatWISE2020 en un cono: añade lo que Gaia no ve, como las enanas marrones frías,
    con movimientos propios en el infrarrojo."""
    q = (f"SELECT source_name, ra, dec, pmra, pmdec, w2mpro FROM catwise_2020 "
         f"WHERE 1 = CONTAINS(POINT('ICRS', ra, dec), CIRCLE('ICRS', {ra:.6f}, {dec:.6f}, {radius:.5f})) "
         f"AND (w1mpro < 15.5 OR w2mpro < 16.5)")
    with catwise_slots:
        t = retry(lambda: Irsa.query_tap(q).to_table())
    # pmra y pmdec vienen en ″/año.
    return [dict(catalog='CatWISE2020', name=f'CatWISE {n}', ra=r, dec=d, pmra=1000 * pa, pmdec=1000 * pd,
                 mjd=CATWISE_MJD, mag=w)
            for n, r, d, pa, pd, w in zip(t['source_name'].tolist(), *(column(t, c).tolist() for c in (
                'ra', 'dec', 'pmra', 'pmdec', 'w2mpro')))]


def merge(gaia, catwise):
    """Añade a Gaia las fuentes de CatWISE que no tiene: las que no quedan a menos de 3″ de una de
    Gaia llevada a la época de CatWISE. CatWISE repite algunos movedores rápidos en detecciones
    separadas hasta 6″; de cada uno queda la entrada más brillante."""
    if not catwise:
        return gaia
    cw = sorted(catwise, key=lambda e: e['mag'])
    c = SkyCoord([e['ra'] for e in cw], [e['dec'] for e in cw], unit='deg')
    new = np.ones(len(cw), bool)
    if gaia:
        g = SkyCoord(*position(*(np.array([e[k] for e in gaia]) for k in ('ra', 'dec', 'pmra', 'pmdec', 'mjd')),
                               CATWISE_MJD), unit='deg')
        new = c.match_to_catalog_sky(g)[1].arcsec >= 3
    kept = []
    for i in np.flatnonzero(new):
        if not kept or c[i].separation(c[kept]).arcsec.min() >= 6:
            kept.append(i)
    return gaia + [cw[i] for i in kept]
