# SPHEREx Sky Timeline

Reproductor del cielo infrarrojo de SPHEREx para el reto *Planet X and SPHEREx* (NASA Space Apps 2026). Muestra cada punto del cielo como un video: una línea de tiempo con todas las visitas de SPHEREx, seis canales de color, diferencia y suma de épocas, espectro y movimiento propio de cualquier fuente al hacer clic, y una capa de catálogo (Gaia DR3 y CatWISE2020) para confirmar lo que se ve moverse.

El diseño y las decisiones de producto están en [`SPHEREx Sky Timeline diseño y features.md`](SPHEREx%20Sky%20Timeline%20diseño%20y%20features.md).

## Ejecutar

Requiere Nix con flakes (el entorno trae Go y las herramientas de compilación).

```sh
nix develop          # o direnv allow
build-web            # compila el motor a WebAssembly y copia la interfaz en web/
serve-web            # proxy + servidor web en http://localhost:8080 (PORT=… para cambiarlo)
```

Sin proxy (por ejemplo, sirviendo `web/` con cualquier servidor estático) la app usa una simulación fiel de SPHEREx. Con el proxy usa imágenes reales; `?data=sim` en la URL fuerza la simulación.

El motor también corre en escritorio: `go run .` (simulación) o `SPHEREX_API=http://localhost:8080 go run .` (datos reales a través de un proxy en marcha).

Pruebas: `go test ./...`.

## Estructura

| Ruta | Qué hay |
|---|---|
| `main.go`, `app.go`, `compose.go`, `viewer.go`, `archive.go` | Motor (Go + raylib): estado, carga de imágenes, composición (época, diferencia, suma), dibujo del visor, selección y medición. |
| `bridge_js.go`, `bridge_native.go` | Puente con la página web (`syscall/js`) o con el teclado en escritorio. |
| `internal/sky` | Modelo de datos: campo, visitas, imágenes, simulación, reproyección de imágenes reales, fotometría y seguimiento. |
| `internal/wcs` | Coordenadas: proyección TAN-SIP y tabla de longitudes de onda WAVE-TAB. |
| `internal/fits` | Lectura y escritura mínima de FITS. |
| `internal/irsa` | Cliente de IRSA: SIA, TAP, recortes, CatWISE, caché en disco. |
| `internal/gaia` | Gaia DR3 desde el CDN de la ESA: HEALPix, descarga por rangos y teselas. |
| `cmd/proxy` | Proxy HTTP: sirve la web y la API `/api/*`. |
| `ui/` | Interfaz HTML/CSS/JS (se copia a `web/` al compilar). |
| `docs/` | Documentación técnica y notebook explicativo. |

## Documentación

- [`docs/protocolos-y-datos.md`](docs/protocolos-y-datos.md): arquitectura, protocolos y métodos de transferencia, API del proxy, formatos, caché, concurrencia y tolerancia a fallos.
- [`docs/operaciones.ipynb`](docs/operaciones.ipynb): notebook en Python que reproduce paso a paso cada operación que hace el código Go, con datos reales y comparándola con astropy.

## Fuentes de datos

- SPHEREx DR1 y QR3: [IRSA](https://irsa.ipac.caltech.edu/Missions/spherex.html) (SIA, TAP y servicio de recortes).
- Gaia DR3: archivos masivos del [CDN de la ESA](https://cdn.gea.esac.esa.int/?prefix=Gaia/gdr3/).
- CatWISE2020: [IRSA](https://irsa.ipac.caltech.edu/) (TAP).
- Métodos basados en los [tutoriales de IRSA para SPHEREx y WISE](https://caltech-ipac.github.io/irsa-tutorials/).
