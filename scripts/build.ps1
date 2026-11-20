param(
    [ValidateSet("amd64", "arm64", "all")]
    [string]$Arch = "amd64",

    [switch]$Installer,
    [switch]$Sign,
    [switch]$SkipTests
)

$ErrorActionPreference = "Stop"

$root = Split-Path -Parent $PSScriptRoot
Set-Location $root

$wails = (Get-Command wails -ErrorAction Stop).Source
$config = Get-Content -LiteralPath (Join-Path $root "wails.json") -Raw | ConvertFrom-Json
$version = $config.info.productVersion
if (-not $version) { throw "wails.json 缺少 info.productVersion" }

$dist = Join-Path $root "dist"
New-Item -ItemType Directory -Force -Path $dist | Out-Null

if (-not $SkipTests) {
    Write-Host "== go test ./internal/..."
    & go test ./internal/...
    if ($LASTEXITCODE -ne 0) { throw "go test 失败" }
    Write-Host "== go vet ./..."
    & go vet ./...
    if ($LASTEXITCODE -ne 0) { throw "go vet 失败" }
}

function Assert-VersionInfo([string]$path, [string]$expected, [string]$label) {
    $info = (Get-Item -LiteralPath $path).VersionInfo
    $product = ([string]$info.ProductVersion).Trim()
    $name = ([string]$info.ProductName).Trim()
    if (-not $product.StartsWith($expected)) {
        throw "$label 文件版本元数据错误：ProductVersion='$product'，期望 '$expected'"
    }
    if (-not $name) {
        throw "$label 缺少 ProductName 元数据"
    }
}

function Invoke-BuildArch([string]$a) {
    Write-Host "== building windows/$a"
    & $wails build -clean -trimpath -platform "windows/$a" -ldflags "-X main.Version=$version"
    if ($LASTEXITCODE -ne 0) { throw "wails build windows/$a 失败" }

    $built = Join-Path $root "build\bin\komi-go.exe"
    if (-not (Test-Path -LiteralPath $built)) { throw "构建产物不存在：$built" }
    $target = Join-Path $dist "KomiGo-$version-$a.exe"
    Copy-Item -LiteralPath $built -Destination $target -Force
    Assert-VersionInfo $target $version "exe windows/$a"
    Write-Host "   -> $target"
    if ($Sign) {
        & (Join-Path $PSScriptRoot "sign.ps1") -Files @($target)
        if ($LASTEXITCODE -ne 0) { throw "签名应用失败" }
    }
    return $target
}

function Find-InnoCompiler {
    $cmd = Get-Command iscc.exe -ErrorAction SilentlyContinue
    if ($cmd) { return $cmd.Source }
    $paths = @(
        "$env:ProgramFiles\Inno Setup 6\ISCC.exe",
        "${env:ProgramFiles(x86)}\Inno Setup 6\ISCC.exe",
        "$env:LOCALAPPDATA\Programs\Inno Setup 6\ISCC.exe",
        "$env:LOCALAPPDATA\Programs\Inno\ISCC.exe"
    )
    foreach ($p in $paths) { if (Test-Path -LiteralPath $p) { return $p } }
    throw "找不到 ISCC.exe。安装 Inno Setup 6（https://jrsoftware.org/isinfo.php），或把 ISCC 加入 PATH。"
}

function Invoke-Installer([string]$a) {
    $iscc = Find-InnoCompiler
    $define = if ($a -eq "arm64") { "MyArchIsArm64=1" } else { "MyArchIsAmd64=1" }
    Write-Host "== Inno Setup windows/$a"
    & $iscc "/DMyAppVersion=$version" "/DMyArch=$a" "/D$define" (Join-Path $root "packaging\komi-go.iss")
    if ($LASTEXITCODE -ne 0) { throw "Inno Setup 编译失败 windows/$a" }

    $setup = Join-Path $dist "KomiGo-$version-$a-setup.exe"
    if (-not (Test-Path -LiteralPath $setup)) { throw "安装包未生成：$setup" }
    Assert-VersionInfo $setup $version "setup windows/$a"
    Write-Host "   -> $setup"
    if ($Sign) {
        & (Join-Path $PSScriptRoot "sign.ps1") -Files @($setup)
        if ($LASTEXITCODE -ne 0) { throw "签名安装包失败" }
    }
}

$targets = if ($Arch -eq "all") { @("amd64", "arm64") } else { @($Arch) }
foreach ($a in $targets) {
    Invoke-BuildArch $a | Out-Null
    if ($Installer) { Invoke-Installer $a }
}

Get-ChildItem -LiteralPath $dist -File | Sort-Object Name |
    Select-Object Name, Length, LastWriteTime |
    Format-Table -AutoSize
