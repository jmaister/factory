# Release de la factory

## Qué se publica

Un release de GitHub con, para cada plataforma:

| Sistema | Arquitectura | Archivo |
|---|---|---|
| Linux | amd64, arm64 | `factory_vX.Y.Z_linux_<arch>.tar.gz` |
| macOS | amd64, arm64 | `factory_vX.Y.Z_darwin_<arch>.tar.gz` |
| Windows | amd64, arm64 | `factory_vX.Y.Z_windows_<arch>.zip` |

Más `checksums.txt` (SHA-256). El binario lleva la versión dentro: `factory version`.

## Cómo sale una versión

1. Actualizar el código y commitear.
2. Crear el tag `vX.Y.Z` y subirlo al remoto de Forgejo.
3. El workflow `.forgejo/workflows/release.yml` compila con `make -f dist.mk dist VERSION=vX.Y.Z` y publica en GitHub.

Para compilar en local sin publicar: `make -f dist.mk dist VERSION=v0.1.0` (deja los archivos en `dist/`).

## Configuración necesaria (una vez)

En Forgejo, en el repo de la factory:
- **Variable** `GITHUB_REPO` = `owner/factory` (el repo de GitHub donde se publican los releases).
- **Secret** `GH_RELEASE_TOKEN` = token de GitHub con **Contents: Read and write** sobre ese repo.

## Instalación en una línea

Linux y macOS:

```bash
curl -fsSL https://raw.githubusercontent.com/OWNER/factory/main/install.sh | bash
```

Windows (PowerShell):

```powershell
irm https://raw.githubusercontent.com/OWNER/factory/main/install.ps1 | iex
```

Variables opcionales: `FACTORY_REPO` (owner/repo), `FACTORY_VERSION` (p. ej. `v0.1.0`), `PREFIX` (por defecto `~/.local`, solo Linux/macOS).

- Linux y macOS: instala en `$PREFIX/bin/factory`.
- Windows: instala en `%LOCALAPPDATA%\factory\bin\factory.exe` y lo añade al PATH del usuario.

Los scripts comprueban el checksum antes de instalar.

## Pendiente antes del primer release

- Crear el repo de GitHub de la factory, y sustituir `OWNER/factory` en `install.sh`, `install.ps1` y este documento.
- Crear el repo espejo en Forgejo y el token `GH_RELEASE_TOKEN`.
- Decidir si el repo de GitHub es público (los instaladores descargan sin autenticación).
- Crear el tag `v0.1.0` (acción visible fuera de tu equipo: pendiente de confirmar).
