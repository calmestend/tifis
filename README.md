# SPHEREx Sky Timeline

Reproductor del cielo infrarrojo de SPHEREx para el reto *Planet X and SPHEREx* (NASA Space Apps 2026). Muestra cada punto del cielo como un video: una línea de tiempo con todas las visitas de SPHEREx, seis canales de color, diferencia y suma de épocas, espectro y movimiento propio de cualquier fuente al hacer clic, y una capa de catálogo (Gaia DR3 y CatWISE2020) para confirmar lo que se ve moverse.

El diseño y las decisiones de producto están en [`SPHEREx Sky Timeline diseño y features.md`](SPHEREx%20Sky%20Timeline%20diseño%20y%20features.md).

## Ejecutar

En macOS, `./ejecutar.command` lo hace todo: instala Go y Python si faltan, compila el motor a WebAssembly, crea `.venv` con las dependencias de `requirements.txt` y abre http://localhost:8080.

Con Nix (flakes), el entorno trae Go, Python y las librerías:

```sh
nix develop          # o direnv allow
build-web            # compila el motor a WebAssembly y copia la interfaz en web/
serve-web            # servidor Python + web en http://localhost:8080 (PORT=… para cambiarlo)
```

A mano: `python -m venv .venv && .venv/bin/pip install -r requirements.txt` y `.venv/bin/python server/main.py --web web`.

La app necesita el servidor: ahí se hace todo el trabajo científico. Por defecto usa imágenes reales; `?data=sim` en la URL muestra la simulación (también la genera el servidor).

El motor también corre en escritorio con el servidor en marcha: `go run .` (datos reales; `SPHEREX_SIM=1` para la simulación, `SPHEREX_API=…` para otro servidor).

Pruebas: `.venv/bin/python server/test_server.py` (servidor, sin red) y `go test ./...` (motor).

## Estructura

| Ruta | Qué hay |
|---|---|
| `server/main.py` | Servidor HTTP: sirve la web y la API `/api/*`, valida, cachea y precalienta los objetos de prueba. |
| `server/archive.py` | Datos externos: SIA y TAP de IRSA (astroquery), recortes sin la PSF (astropy.io.fits), Gaia DR3 (astroquery.gaia), CatWISE2020, fusión de catálogos y caché en disco. |
| `server/science.py` | Ciencia: rejilla del campo, reproyección TAN-SIP y longitud de onda WAVE-TAB (astropy.wcs), banderas, fotometría (photutils), centroides, seguimiento y movimiento propio. |
| `server/sim.py` | Simulación de SPHEREx con la misma estructura que los datos reales. |
| `server/test_server.py`, `server/testdata/` | Comprobaciones sin red, con un recorte real de Barnard. |
| `main.go`, `app.go`, `compose.go`, `viewer.go`, `archive.go` | Motor (Go + raylib, WebAssembly): pide los fotogramas ya reproyectados, compone (época, diferencia, suma), colorea y dibuja el visor. |
| `bridge_js.go`, `bridge_native.go` | Puente con la página web (`syscall/js`) o con el teclado en escritorio. |
| `internal/sky` | Lo que el motor guarda de un campo: visitas, fotogramas, banderas, catálogo, movimiento ajustado y la rejilla para dibujar. |
| `ui/` | Interfaz HTML/CSS/JS (se copia a `web/` al compilar). |
| `docs/` | Documentación técnica y notebook explicativo. |

## Documentación

- [`docs/protocolos-y-datos.md`](docs/protocolos-y-datos.md): arquitectura, protocolos y métodos de transferencia, API del servidor, formatos, caché, concurrencia y tolerancia a fallos.
- [`docs/operaciones.ipynb`](docs/operaciones.ipynb): notebook que reproduce a mano, paso a paso y con datos reales, cada operación del servidor y la compara con astropy y photutils.

## Fuentes de datos

- SPHEREx DR1 y QR3: [IRSA](https://irsa.ipac.caltech.edu/Missions/spherex.html) (SIA, TAP y servicio de recortes).
- Gaia DR3: [archivo de Gaia de la ESA](https://gea.esac.esa.int/archive/) (TAP, tabla `gaiadr3.gaia_source_lite`).
- CatWISE2020: [IRSA](https://irsa.ipac.caltech.edu/) (TAP).
- Métodos basados en los [tutoriales de IRSA para SPHEREx y WISE](https://caltech-ipac.github.io/irsa-tutorials/).
