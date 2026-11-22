# Instala la factory desde un release de GitHub (Windows, amd64 y arm64).
#   irm https://raw.githubusercontent.com/jmaister/factory/main/install.ps1 | iex
# Variables opcionales: FACTORY_REPO (owner/repo), FACTORY_VERSION (p. ej. v0.1.0)
$ErrorActionPreference = 'Stop'

$Repo = if ($env:FACTORY_REPO) { $env:FACTORY_REPO } else { 'jmaister/factory' }
$Version = if ($env:FACTORY_VERSION) { $env:FACTORY_VERSION } else { 'v0.1.0' }

$arch = if ($env:PROCESSOR_ARCHITECTURE -eq 'ARM64') { 'arm64' } else { 'amd64' }
$name = "factory_${Version}_windows_${arch}"
$base = "https://github.com/$Repo/releases/download/$Version"
$tmp = Join-Path $env:TEMP "factory-install-$([guid]::NewGuid())"
New-Item -ItemType Directory -Force -Path $tmp | Out-Null

try {
    Write-Host "Descargando $name..."
    $zip = Join-Path $tmp "$name.zip"
    Invoke-WebRequest "$base/$name.zip" -OutFile $zip
    Invoke-WebRequest "$base/checksums.txt" -OutFile (Join-Path $tmp 'checksums.txt')

    Write-Host 'Comprobando checksum...'
    $line = Get-Content (Join-Path $tmp 'checksums.txt') | Where-Object { $_ -match " $name\.zip$" }
    $expected = ($line -split '\s+')[0]
    $actual = (Get-FileHash -Algorithm SHA256 $zip).Hash.ToLower()
    if ($expected -ne $actual) { throw 'El checksum no coincide. No se instala.' }

    Expand-Archive -Path $zip -DestinationPath $tmp -Force
    $dir = Join-Path $env:LOCALAPPDATA 'factory\bin'
    New-Item -ItemType Directory -Force -Path $dir | Out-Null
    Copy-Item (Join-Path $tmp "$name\factory.exe") (Join-Path $dir 'factory.exe') -Force
    Write-Host "Instalado: $dir\factory.exe"

    $userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
    if (($userPath -split ';') -notcontains $dir) {
        [Environment]::SetEnvironmentVariable('Path', "$userPath;$dir", 'User')
        Write-Host 'Añadido al PATH de tu usuario. Abre una terminal nueva para usarlo.'
    }
}
finally {
    Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue
}
