param(
    [Parameter(Mandatory = $true)]
    [string[]]$Files,

    [string]$Pfx = $env:KOMI_SIGN_CERT,
    [string]$PfxPassword = $env:KOMI_SIGN_CERT_PASSWORD,
    [string]$Thumbprint = $env:KOMI_SIGN_THUMBPRINT,
    [string]$Subject = $env:KOMI_SIGN_SUBJECT,
    [string]$TimestampUrl = "https://timestamp.digicert.com"
)

$ErrorActionPreference = "Stop"

function Find-SignTool {
    $cmd = Get-Command signtool.exe -ErrorAction SilentlyContinue
    if ($cmd) { return $cmd.Source }
    $roots = @(
        "${env:ProgramFiles(x86)}\Windows Kits\10\bin",
        "$env:ProgramFiles\Windows Kits\10\bin"
    ) | Where-Object { Test-Path $_ }
    $found = Get-ChildItem -Path $roots -Filter signtool.exe -Recurse -ErrorAction SilentlyContinue |
        Where-Object { $_.FullName -match '\\x64\\signtool\.exe$' } |
        Sort-Object FullName -Descending |
        Select-Object -First 1
    if ($found) { return $found.FullName }
    throw "找不到 signtool.exe。安装 Windows SDK 的 Signing Tools，或把 signtool 加入 PATH。"
}

$signTool = Find-SignTool
$base = @("sign", "/fd", "SHA256", "/td", "SHA256")
if ($TimestampUrl) { $base += @("/tr", $TimestampUrl) }

if ($Pfx) {
    if (-not (Test-Path -LiteralPath $Pfx)) { throw "证书不存在：$Pfx" }
    $base += @("/f", $Pfx)
    if ($PfxPassword) { $base += @("/p", $PfxPassword) }
}
elseif ($Thumbprint) {
    $base += @("/sha1", ($Thumbprint -replace '\s', ''))
}
elseif ($Subject) {
    $base += @("/n", $Subject)
}
else {
    throw "未提供签名材料。设置 KOMI_SIGN_CERT(.pfx)、KOMI_SIGN_CERT_PASSWORD、KOMI_SIGN_THUMBPRINT 或 KOMI_SIGN_SUBJECT。"
}

foreach ($file in $Files) {
    if (-not (Test-Path -LiteralPath $file)) { throw "待签名文件不存在：$file" }
    Write-Host "signtool: $file"
    & $signTool @base $file
    if ($LASTEXITCODE -ne 0) { throw "签名失败：$file (exit $LASTEXITCODE)" }
    & $signTool verify /pa /v $file
    if ($LASTEXITCODE -ne 0) { throw "签名校验失败：$file (exit $LASTEXITCODE)" }
}
