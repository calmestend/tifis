"""Servidor de SPHEREx Sky Timeline: sirve la web (web/) y la API /api/*. Hace todo el trabajo
científico; el motor del navegador solo compone y dibuja.

    GET /api/health                                    {"ok": true}
    GET /api/targets                                   objetos de prueba en su posición de hoy
    GET /api/field?ra&dec&fov[&sim=1]                  plan del campo: visitas e imágenes por canal
    GET /api/frame?ra&dec&fov&path                     imagen real en la rejilla del campo (binario)
    GET /api/frame?ra&dec&fov&sim=1&now&c&v            imagen simulada
    GET /api/catalog?ra&dec&fov[&sim=1&now]            Gaia DR3 y CatWISE2020 del campo
    GET /api/measure?ra&dec&fov&band&visit&x&y[&sim=1&now]
                                                       fotometría y movimiento de una fuente
    GET /api/visits?ra&dec                             imágenes de SPHEREx que cubren la posición
    GET /api/cutout?path&ra&dec&size                   recorte de IRSA sin EPSF (FITS)

Las posiciones se redondean a 0.0005° (1.8″), igual que en el motor: la posición de hoy de un
objeto rápido cambia el sexto decimal cada pocas horas y sin redondeo las claves de la caché
casi nunca coincidirían con las del precalentamiento.
"""
import argparse
import contextlib
import json
import logging
import math
import os
import socket
import sys
import threading
import time
from datetime import datetime, timezone
from functools import lru_cache, partial
from http.server import SimpleHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import parse_qs, urlsplit

import archive
import science
import sim

VISITS_TTL = 24 * 3600  # el archivo recibe imágenes cada semana: una lista de un día vale
CACHE: archive.Cache = None  # main() la crea
log = logging.getLogger('server')


class ClientError(Exception):
    """Un error de la petición; lo demás viene de un servicio externo (o es un fallo nuestro): 502."""
    status = 400


class BadRequest(ClientError):
    pass


class NotFound(ClientError):
    status = 404


class Unreadable(ClientError):
    """Un recorte que no se puede leer: el motor lo muestra como hueco en vez de reintentarlo."""
    status = 422


def rnd(v):
    """Redondea a 0.0005°, alejándose del cero en los empates como math.Round de Go."""
    return math.copysign(math.floor(abs(v) * 2000 + 0.5), v) / 2000


def num(q, name, lo=-math.inf, hi=math.inf):
    try:
        v = float(q[name])
    except (KeyError, ValueError):
        raise BadRequest(f'{name} must be a number') from None
    if not lo <= v <= hi:
        raise BadRequest(f'{name} must be in [{lo}, {hi}]')
    return v


def position(q):
    ra, dec = num(q, 'ra'), num(q, 'dec')
    if not (0 <= ra < 360 and -90 <= dec <= 90):
        raise BadRequest('ra must be in [0, 360) and dec in [-90, 90] degrees')
    return rnd(ra) % 360, rnd(dec)


def field_args(q):
    ra, dec = position(q)
    return ra, dec, min(12.0, max(4.0, num(q, 'fov')))


def is_sim(q):
    return q.get('sim') == '1'


def sim_now(q):
    return int(num(q, 'now'))


def visits_key(ra, dec):
    return f'visits {ra:.4f} {dec:.4f}'


def cutout_key(path, ra, dec, size):
    return f'cutout {path} {ra:.6f} {dec:.6f} {size:.5f}'


def frame_key(path, ra, dec, fov):
    return f'frame1 {path} {ra:.6f} {dec:.6f} {fov:g}'


def gaia_key(ra, dec):
    return f'gaia {ra:.4f} {dec:.4f} {archive.CATALOG_RADIUS:.3f}'


def catwise_key(ra, dec):
    return f'catwise {ra:.4f} {dec:.4f} {archive.CATALOG_RADIUS:.3f}'


# ---------- Datos ----------

def visits(ra, dec):
    """La lista de imágenes, por SIA o, si falla, por TAP. Devuelve (JSON, acierto de caché)."""
    def fetch():
        source = 'SIA'
        try:
            v = archive.visits_sia(ra, dec)
        except Exception as e:
            log.warning('SIA failed, trying TAP: %s', e)
            source, v = 'TAP', archive.visits_tap(ra, dec)
        return json.dumps(dict(ra=ra, dec=dec, source=source, fetched=datetime.now(timezone.utc).isoformat(),
                               visits=v)).encode()
    return CACHE.get(visits_key(ra, dec), VISITS_TTL, fetch)


def real_plan(ra, dec, fov, data):
    return science.plan(json.loads(data)['visits'], ra, dec, fov, science.now_mjd())


@lru_cache(maxsize=3000)  # ponytail: ~220 KB por imagen a 12′; un tope por bytes si falta memoria
def stored_frame(key):
    """Una imagen ya llevada a la rejilla y guardada; KeyError si todavía no está."""
    data = CACHE.lookup(key)
    if data is None:
        raise KeyError(key)
    return science.Frame.load(data)


def catalog(ra, dec):
    """Gaia DR3 y CatWISE2020, cada uno con su caché. CatWISE es lento: tiene 20 s de gracia una vez
    está Gaia; si no llega, la respuesta sale sin él y la consulta sigue en segundo plano para la
    próxima vez. Devuelve (fuentes, catálogos que faltaron)."""
    r = archive.CATALOG_RADIUS
    cw = archive.in_background(lambda: CACHE.get(
        catwise_key(ra, dec), 0, lambda: json.dumps(archive.catwise(ra, dec, r)).encode())[0])
    gaia = json.loads(CACHE.get(gaia_key(ra, dec), 0, lambda: json.dumps(archive.gaia(ra, dec, r)).encode())[0])
    try:
        return archive.merge(gaia, json.loads(cw.result(timeout=20))), []
    except Exception as e:
        log.warning('catalog %.4f %.4f without CatWISE: %s', ra, dec, e)
        return gaia, ['CatWISE2020']


def cached_catalog(ra, dec):
    """Lo que ya esté guardado de los dos catálogos, sin consultar nada."""
    gaia, cw = CACHE.lookup(gaia_key(ra, dec)), CACHE.lookup(catwise_key(ra, dec))
    return archive.merge(json.loads(gaia) if gaia else [], json.loads(cw) if cw else [])


def with_target(sources, ra, dec, fov):
    """Marca el objeto de prueba del campo entre las fuentes del catálogo y le pone su nombre."""
    t = science.find_target(ra, dec, fov, science.now_mjd())
    k = science.find_in_catalog(sources, t)
    if k >= 0:
        sources = sources[:k] + [sources[k] | dict(name=t['name'])] + sources[k + 1:]
    return sources, k


# ---------- API ----------

def api_visits(q):
    data, hit = visits(*position(q))
    return data, 'application/json', {'X-Cache': hit}


def api_field(q):
    ra, dec, fov = field_args(q)
    if is_sim(q):
        return sim.field(ra, dec, fov, math.floor(science.now_mjd()))[0]
    return real_plan(ra, dec, fov, visits(ra, dec)[0])


def api_frame(q):
    ra, dec, fov = field_args(q)
    if is_sim(q):
        plan = sim.field(ra, dec, fov, sim_now(q))[0]
        c, v = int(num(q, 'c', 0, 5)), int(num(q, 'v', 0, len(plan['visits']) - 1))
        if not plan['files'][c][v]:
            raise NotFound('no image of that channel in that visit')
        return sim.frame(ra, dec, fov, sim_now(q), c, v).payload(), 'application/octet-stream', {}

    path = q.get('path', '')
    m = archive.FILE_RE.search(path)
    if not archive.valid_path(path) or not m:
        raise BadRequest('path must be a SPHEREx archive file')
    key = frame_key(path, ra, dec, fov)

    def build():
        size = science.cutout_size(fov)
        raw, _ = CACHE.get(cutout_key(path, ra, dec, size), 0, lambda: archive.cutout(path, ra, dec, size))
        try:
            return science.process(raw, ra, dec, science.npix(fov), int(m[2]) - 1).dump()
        except Exception as e:
            raise Unreadable(f'unreadable cutout: {e}') from e

    _, hit = CACHE.get(key, 0, build)
    # La ruta lleva la versión de procesado, así que una imagen no cambia nunca.
    return stored_frame(key).payload(), 'application/octet-stream', {
        'X-Cache': hit, 'Cache-Control': 'public, max-age=31536000, immutable'}


def api_catalog(q):
    ra, dec, fov = field_args(q)
    if is_sim(q):
        sources, target = sim.catalog(ra, dec, fov, sim_now(q))
        return dict(sources=sources, target=target, missing=[])
    sources, missing = catalog(ra, dec)
    sources, target = with_target(sources, ra, dec, fov)
    return dict(ra=ra, dec=dec, radius=archive.CATALOG_RADIUS, sources=sources, missing=missing, target=target)


def api_measure(q):
    """Mide con las imágenes que ya se sirvieron (las que el motor tiene); al ir llegando más, el
    motor vuelve a pedir la medida."""
    ra, dec, fov = field_args(q)
    if is_sim(q):
        now = sim_now(q)
        plan = sim.field(ra, dec, fov, now)[0]
        frames = {(c, v): sim.frame(ra, dec, fov, now, c, v)
                  for c, files in enumerate(plan['files']) for v, f in enumerate(files) if f}
        sources, scale = sim.catalog(ra, dec, fov, now)[0], 1
    else:
        data = CACHE.lookup(visits_key(ra, dec))
        if data is None:
            raise BadRequest('open the field first')
        plan = real_plan(ra, dec, fov, data)
        frames = {}
        for c, files in enumerate(plan['files']):
            for v, path in enumerate(files):
                if path:
                    with contextlib.suppress(KeyError):
                        frames[c, v] = stored_frame(frame_key(path, ra, dec, fov))
        sources, scale = with_target(cached_catalog(ra, dec), ra, dec, fov)[0], science.FLUX_SCALE
    n = plan['npix']
    band, visit = int(num(q, 'band', 0, 5)), int(num(q, 'visit', 0, len(plan['visits']) - 1))
    x, y = num(q, 'x', -n, 2 * n), num(q, 'y', -n, 2 * n)
    return science.select(frames, plan, sources, band, visit, x, y, scale)


def api_cutout(q):
    ra, dec = position(q)
    path, size = q.get('path', ''), num(q, 'size')
    if not archive.valid_path(path):
        raise BadRequest('path must be a SPHEREx archive file')
    if not 0 < size <= 0.3:
        raise BadRequest('size must be in (0, 0.3] degrees')
    data, hit = CACHE.get(cutout_key(path, ra, dec, size), 0, lambda: archive.cutout(path, ra, dec, size))
    return data, 'application/fits', {'X-Cache': hit, 'Cache-Control': 'public, max-age=31536000, immutable'}


ROUTES = {
    '/api/health': lambda q: dict(ok=True),
    '/api/targets': lambda q: science.targets_now(),
    '/api/field': api_field,
    '/api/frame': api_frame,
    '/api/catalog': api_catalog,
    '/api/measure': api_measure,
    '/api/visits': api_visits,
    '/api/cutout': api_cutout,
}


class Handler(SimpleHTTPRequestHandler):
    protocol_version = 'HTTP/1.1'
    extensions_map = SimpleHTTPRequestHandler.extensions_map | {'.wasm': 'application/wasm'}

    def do_GET(self):
        url = urlsplit(self.path)
        if not url.path.startswith('/api/'):
            return super().do_GET()
        start = time.monotonic()
        q = {k: v[-1] for k, v in parse_qs(url.query).items()}
        status, ctype, headers = 200, 'application/json', {}
        try:
            route = ROUTES.get(url.path)
            if route is None:
                raise NotFound('no such endpoint')
            body = route(q)
            if isinstance(body, tuple):
                body, ctype, headers = body
            else:
                body = json.dumps(body, allow_nan=False).encode()
        except ClientError as e:
            status, body = e.status, json.dumps(dict(error=str(e))).encode()
            ctype, headers = 'application/json', {}
        except Exception as e:
            # Los errores de red de IRSA o la ESA no necesitan traza; lo demás puede ser un fallo nuestro.
            log.warning('%s: %s', self.path, e, exc_info=not isinstance(e, OSError))
            status = 502
            body = json.dumps(dict(error=str(e) or type(e).__name__)).encode()
            ctype, headers = 'application/json', {}
        try:
            self.send_response(status)
            self.send_header('Content-Type', ctype)
            self.send_header('Content-Length', str(len(body)))
            for k, v in headers.items():
                self.send_header(k, ('HIT' if v else 'MISS') if k == 'X-Cache' else v)
            self.end_headers()
            self.wfile.write(body)
        except (BrokenPipeError, ConnectionResetError):
            pass  # el navegador se fue; el resultado ya quedó en la caché
        log.info('GET %s %d %.0f ms', self.path, status, (time.monotonic() - start) * 1000)

    def end_headers(self):
        if self.path.startswith('/api/'):
            self.send_header('Access-Control-Allow-Origin', '*')
        else:
            self.send_header('Cache-Control', 'no-cache')  # una recompilación se ve al recargar
        super().end_headers()

    def log_request(self, code='-', size='-'):
        if not self.path.startswith('/api/') and int(code) >= 400:
            log.info('GET %s %s', self.path, code)

    def log_message(self, *args):
        pass


def warm():
    """Pide de antemano las listas de imágenes y luego los catálogos de los objetos de prueba, en las
    posiciones que pedirá la app, de una en una para dejar sitio a las peticiones de la app."""
    now = science.now_mjd()
    for name, fetch in (('visits', visits), ('catalog', catalog)):
        for t in science.TARGETS:
            ra, dec = science.target_position(t, now)
            start = time.monotonic()
            try:
                fetch(rnd(ra) % 360, rnd(dec))
                log.info('warm-up %s %s: %.0f s', t['id'], name, time.monotonic() - start)
            except Exception as e:
                log.warning('warm-up %s %s: %s', t['id'], name, e)


def main():
    if sys.platform == 'darwin':
        cache_home = os.path.expanduser('~/Library/Caches')
    else:
        cache_home = os.environ.get('XDG_CACHE_HOME') or os.path.expanduser('~/.cache')
    p = argparse.ArgumentParser(description='Sirve SPHEREx Sky Timeline y su API.')
    p.add_argument('--addr', default=':8080', help='dirección de escucha, [host]:puerto')
    p.add_argument('--web', default='web', help='carpeta con la web compilada')
    p.add_argument('--cache', default=os.path.join(cache_home, 'spherex-proxy'), help='carpeta de la caché')
    p.add_argument('--parallel', type=int, default=8, help='recortes simultáneos a IRSA')
    args = p.parse_args()

    logging.basicConfig(level=logging.INFO, format='%(asctime)s %(message)s')
    # Nada se queda colgado para siempre: lo más lento (TAP de CatWISE) tarda unos minutos.
    socket.setdefaulttimeout(600)
    global CACHE
    CACHE = archive.Cache(args.cache)
    archive.cutout_slots = threading.BoundedSemaphore(args.parallel)
    host, _, port = args.addr.rpartition(':')
    srv = ThreadingHTTPServer((host, int(port)), partial(Handler, directory=args.web))
    threading.Thread(target=warm, daemon=True).start()
    log.info('serving %s and the API on %s (cache %s)', args.web, args.addr, args.cache)
    with contextlib.suppress(KeyboardInterrupt):
        srv.serve_forever()


if __name__ == '__main__':
    main()
