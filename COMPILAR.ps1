$ErrorActionPreference = 'Stop'
$projectRoot = $PSScriptRoot
$workstation = Join-Path $projectRoot 'netflix-workstation'
$downloader = Join-Path $projectRoot 'netflix-downloader'
$assets = Join-Path $workstation 'assets'
$vendorDownloader = Join-Path $workstation 'vendor\local.netflix\netflix-downloader'

try {
    if (Test-Path -LiteralPath $vendorDownloader) {
        Remove-Item -LiteralPath $vendorDownloader -Recurse -Force
    }
    New-Item -ItemType Directory -Path $vendorDownloader -Force | Out-Null
    Get-ChildItem -LiteralPath $downloader -File -Filter '*.go' |
        Where-Object { $_.Name -notlike '*_test.go' } |
        Copy-Item -Destination $vendorDownloader -Force

    New-Item -ItemType Directory -Path $assets -Force | Out-Null
    Push-Location $workstation
    try {
        go build -mod=vendor -trimpath -ldflags '-s -w -H=windowsgui' -o 'Netflix Downloader.exe' './cmd/netflix'
        if ($LASTEXITCODE -ne 0) { throw 'No se pudo compilar Netflix Downloader.' }
        go build -mod=vendor -trimpath -ldflags '-s -w' -o 'assets\cdm-portable.exe' './cmd/cdmportable'
        if ($LASTEXITCODE -ne 0) { throw 'No se pudo compilar el motor portátil.' }
        go build -mod=vendor -trimpath -ldflags '-s -w' -o 'assets\drm-probe.exe' './cmd/drmprobe'
        if ($LASTEXITCODE -ne 0) { throw 'No se pudo compilar la herramienta DRM.' }
    } finally { Pop-Location }
} finally {
    if (Test-Path -LiteralPath $vendorDownloader) {
        Remove-Item -LiteralPath $vendorDownloader -Recurse -Force
    }
    $vendorLocal = Join-Path $workstation 'vendor\local.netflix'
    if ((Test-Path -LiteralPath $vendorLocal -PathType Container) -and
        -not (Get-ChildItem -LiteralPath $vendorLocal -Force | Select-Object -First 1)) {
        Remove-Item -LiteralPath $vendorLocal -Force
    }
}
