#!/usr/bin/env bash
# Instala la factory desde un release de GitHub (Linux y macOS, amd64 y arm64).
#   curl -fsSL https://raw.githubusercontent.com/OWNER/factory/main/install.sh | bash
# Variables opcionales: FACTORY_REPO (owner/repo), FACTORY_VERSION (p. ej. v0.1.0), PREFIX (por defecto ~/.local)
set -euo pipefail

REPO="${FACTORY_REPO:-OWNER/factory}"
VERSION="${FACTORY_VERSION:-v0.1.0}"
PREFIX="${PREFIX:-$HOME/.local}"

case "$(uname -s)" in
  Linux) os=linux ;;
  Darwin) os=darwin ;;
  *) echo "Sistema no soportado: $(uname -s). En Windows usa install.ps1." >&2; exit 1 ;;
esac
case "$(uname -m)" in
  x86_64|amd64) arch=amd64 ;;
  arm64|aarch64) arch=arm64 ;;
  *) echo "Arquitectura no soportada: $(uname -m)" >&2; exit 1 ;;
esac

name="factory_${VERSION}_${os}_${arch}"
base="https://github.com/${REPO}/releases/download/${VERSION}"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

echo "Descargando ${name}..."
curl -fsSL "${base}/${name}.tar.gz" -o "${tmp}/${name}.tar.gz"
curl -fsSL "${base}/checksums.txt" -o "${tmp}/checksums.txt"

echo "Comprobando checksum..."
expected="$(grep " ${name}.tar.gz\$" "${tmp}/checksums.txt" | cut -d' ' -f1)"
if command -v sha256sum >/dev/null 2>&1; then
  actual="$(sha256sum "${tmp}/${name}.tar.gz" | cut -d' ' -f1)"
else
  actual="$(shasum -a 256 "${tmp}/${name}.tar.gz" | cut -d' ' -f1)"
fi
if [ "$expected" != "$actual" ]; then
  echo "El checksum no coincide. No se instala." >&2
  exit 1
fi

tar -xzf "${tmp}/${name}.tar.gz" -C "$tmp"
mkdir -p "${PREFIX}/bin"
install -m 755 "${tmp}/${name}/factory" "${PREFIX}/bin/factory"
echo "Instalado: ${PREFIX}/bin/factory ($("${PREFIX}/bin/factory" version))"

case ":$PATH:" in
  *":${PREFIX}/bin:"*) ;;
  *) echo "Añade ${PREFIX}/bin a tu PATH para usarlo." ;;
esac
