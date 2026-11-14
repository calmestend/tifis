#!/bin/bash
# Compila SPHEREx Sky Timeline a WebAssembly y levanta el servidor Python en http://localhost:8080
export PATH="/opt/homebrew/bin:/usr/local/bin:/usr/local/go/bin:$PATH"
cd "$(dirname "$0")" || exit 1
LOG="ejecucion.log"
: > "$LOG"
exec > >(tee -a "$LOG") 2>&1
trap 'echo "✘ ERROR en línea $LINENO"; echo "=== FIN (con error)"; read -r -p "Enter para cerrar"' ERR
echo "=== INICIO $(date '+%F %T')"

if ! command -v go >/dev/null 2>&1; then
  echo "Go no está instalado. Instalando con Homebrew..."
  brew install go 2>&1 || { echo "✘ No se pudo instalar Go"; exit 1; }
fi
go version 2>&1

set -eE
mkdir -p web
RL="$(go list -m -f '{{.Dir}}' github.com/gen2brain/raylib-go/raylib)"
echo "RL=$RL"; ls "$RL/libs/wasm" 2>&1
echo "Compilando a WebAssembly..."
GOOS=js GOARCH=wasm CGO_ENABLED=0 go build -ldflags="-s -w" -o web/app.wasm . 2>&1
install -m 644 "$(go env GOROOT)/lib/wasm/wasm_exec.js" web/wasm_exec.js
gzip -dc "$RL/libs/wasm/raylib.js.gz"   > web/raylib.js
gzip -dc "$RL/libs/wasm/raylib.wasm.gz" > web/raylib.wasm
install -m 644 "$RL/libs/wasm/raylib-go.js" web/raylib-go.js
install -m 644 ui/* web/
tar -czf web/resources.tar.gz resources
# El servidor es Python (server/): astropy, astroquery y photutils necesitan 3.11 o más.
PY="$(command -v python3.13 || command -v python3.12 || command -v python3.11 || command -v python3 || true)"
if [ -z "$PY" ] || ! "$PY" -c 'import sys; sys.exit(sys.version_info < (3, 11))'; then
  echo "Hace falta Python 3.11 o más. Instalando con Homebrew..."
  brew install python@3.12 2>&1
  PY="$(brew --prefix)/bin/python3.12"
fi
[ -x .venv/bin/python ] || "$PY" -m venv .venv 2>&1
.venv/bin/pip install -q -r requirements.txt 2>&1
echo "✓ Build listo. Servidor en http://localhost:8080 (Ctrl+C para detener)"
( sleep 6; open http://localhost:8080 ) &
.venv/bin/python server/main.py --addr ":8080" --web web 2>&1
