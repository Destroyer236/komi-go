# Builds the Inno Setup installer for an already compiled KomiGo exe.
#
# Usage:
#   .\scripts\build-installer.ps1                 # uses wails.json's productVersion
#   .\scripts\build-installer.ps1 -Version 0.3.2
#
# Expects dist\KomiGo-<version>-<arch>.exe (produced by scripts\build.ps1) and
# Inno Setup 6 (ISCC.exe). The output lands next to the exe:
#   dist\KomiGo-<version>-<arch>-setup.exe

[CmdletBinding()]
param(
    [string]$Version,
    [string]$Arch = "amd64"
)

$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot

if (-not $Version) {
    $wailsJson = Join-Path $root "wails.json"
    $Version = (Get-Content $wailsJson -Raw | ConvertFrom-Json).info.productVersion
}
if (-not $Version) { throw "无法确定版本号：传 -Version 或在 wails.json 里设置 info.productVersion" }

$exe = Join-Path $root "dist\KomiGo-$Version-$Arch.exe"
if (-not (Test-Path $exe)) {
    throw "缺少 $exe；先运行 scripts\build.ps1 -Arch $Arch"
}

$isccCandidates = @(
    (Join-Path $env:LOCALAPPDATA "Programs\Inno\ISCC.exe"),
    "C:\Program Files (x86)\Inno Setup 6\ISCC.exe",
    "C:\Program Files\Inno Setup 6\ISCC.exe"
)
$iscc = $isccCandidates | Where-Object { Test-Path $_ } | Select-Object -First 1
if (-not $iscc) {
    throw "未找到 ISCC.exe；请安装 Inno Setup 6（winget install JRSoftware.InnoSetup）"
}

$iss = Join-Path $root "installer\komi-go.iss"
$isl = Join-Path $root "installer\ChineseSimplified.isl"
$license = Join-Path $root "LICENSE"
foreach ($f in @($iss, $isl, $license)) {
    if (-not (Test-Path $f)) { throw "缺少 $f" }
}

Write-Host "ISCC      : $iscc"
Write-Host "Version   : $Version ($Arch)"
Write-Host "Source exe: $exe"

& $iscc `
    "/DAppVersion=$Version" `
    "/DSourceExe=$exe" `
    "/DSourceDir=$root" `
    "/DLicenseFile=$license" `
    "/DChineseIsl=$isl" `
    $iss
if ($LASTEXITCODE -ne 0) { throw "ISCC 编译失败，退出码 $LASTEXITCODE" }

$setup = Join-Path $root "dist\KomiGo-$Version-$Arch-setup.exe"
if (-not (Test-Path $setup)) { throw "编译结束但没有生成 $setup" }

$item = Get-Item $setup
$hash = (Get-FileHash $setup -Algorithm SHA256).Hash
Write-Host ""
Write-Host ("安装包: {0}" -f $setup)
Write-Host ("大小  : {0:N0} 字节 ({1:N1} MB)" -f $item.Length, ($item.Length / 1MB))
Write-Host ("SHA256: {0}" -f $hash)
