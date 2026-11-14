{
  description = "Development shell for spherex (raylib-go + wasm)";

  inputs = {
    nixpkgs = {
      type = "github";
      owner = "NixOS";
      repo = "nixpkgs";
      ref = "nixpkgs-unstable";
    };
  };

  outputs = {nixpkgs, ...}: let
    inherit (nixpkgs) lib;

    makeDefault = x: {
      default = x;
    };
    pkgsFor = let
      perSystem = system:
        import nixpkgs {
          inherit system;
          config.allowUnfree = true;
        };
    in
      perSystem
      |> lib.genAttrs lib.systems.flakeExposed;
    forAllSystems = f: let
      mapSystem = _system: f;
    in
      pkgsFor
      |> lib.mapAttrs mapSystem;
    # raylib-go ships a prebuilt raylib wasm module (raylib/libs/wasm), so the
    # web build needs no C toolchain: only Go and the files from the module.
    makeBuildWeb = pkgs:
      pkgs.writeShellApplication {
        name = "build-web";
        runtimeInputs = with pkgs; [go gzip gnutar coreutils];
        text = ''
          OUT="''${OUT:-web}"
          TAGS="''${TAGS:-}"
          mkdir -p "$OUT"

          RL="$(go list -m -f '{{.Dir}}' github.com/gen2brain/raylib-go/raylib)"
          GOROOT="$(go env GOROOT)"

          GOOS=js GOARCH=wasm CGO_ENABLED=0 go build -tags "$TAGS" -ldflags="-s -w" -o "$OUT/app.wasm" .

          install -m 644 "$GOROOT/lib/wasm/wasm_exec.js" "$OUT/wasm_exec.js"

          # WebGL 1 by default, TAGS=es3 for WebGL 2
          MODULE=raylib
          case " ''${TAGS//,/ } " in *" es3 "*) MODULE=raylib-es3 ;; esac

          gzip -dc "$RL/libs/wasm/$MODULE.js.gz" > "$OUT/raylib.js"
          gzip -dc "$RL/libs/wasm/$MODULE.wasm.gz" > "$OUT/raylib.wasm"
          install -m 644 "$RL/libs/wasm/raylib-go.js" "$OUT/raylib-go.js"
          # HTML/CSS/JS shell around the canvas
          install -m 644 ui/* "$OUT/"

          if [ -d resources ]; then
            tar -czf "$OUT/resources.tar.gz" resources
          fi
        '';
      };
    # Serves the app and the IRSA proxy from one origin: IRSA sends no CORS
    # headers, so the browser cannot query it directly.
    makeServeWeb = pkgs:
      pkgs.writeShellApplication {
        name = "serve-web";
        runtimeInputs = with pkgs; [go];
        text = ''
          exec go run ./cmd/proxy -addr ":''${PORT:-8080}" -web "''${OUT:-web}" "$@"
        '';
      };
    makeDevShell = pkgs: let
      # Only needed to run the game natively on Linux (go run .)
      desktopLibs = with pkgs;
        lib.optionals stdenv.hostPlatform.isLinux [
          libGL
          libx11
          libxi
          libxcursor
          libxrandr
          libxinerama
          wayland
          libxkbcommon
        ];
    in
      pkgs.mkShell {
        packages = with pkgs; [
          go
          go-tools
          gopls
          gotools
          golangci-lint
          gomodifytags
          (makeBuildWeb pkgs)
          (makeServeWeb pkgs)
        ];

        buildInputs = desktopLibs;
        LD_LIBRARY_PATH = lib.makeLibraryPath desktopLibs;
      }
      |> makeDefault;
  in {
    devShells = makeDevShell |> forAllSystems;
  };
}
