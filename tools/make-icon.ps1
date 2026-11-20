$ErrorActionPreference = 'Stop'
Add-Type -AssemblyName System.Drawing

$outDir = Join-Path (Split-Path $PSScriptRoot -Parent) 'build\windows'
New-Item -ItemType Directory -Force -Path $outDir | Out-Null

function New-IconBitmap([int]$size) {
    $bmp = New-Object System.Drawing.Bitmap $size, $size, ([System.Drawing.Imaging.PixelFormat]::Format32bppArgb)
    $g = [System.Drawing.Graphics]::FromImage($bmp)
    $g.SmoothingMode = [System.Drawing.Drawing2D.SmoothingMode]::HighQuality
    $g.PixelOffsetMode = [System.Drawing.Drawing2D.PixelOffsetMode]::HighQuality
    $g.TextRenderingHint = [System.Drawing.Text.TextRenderingHint]::AntiAliasGridFit
    $g.Clear([System.Drawing.Color]::Transparent)

    $r = [Math]::Max(2, [int]($size * 0.23))
    $d = $r * 2
    $path = New-Object System.Drawing.Drawing2D.GraphicsPath
    $path.AddArc(0, 0, $d, $d, 180, 90)
    $path.AddArc($size - $d, 0, $d, $d, 270, 90)
    $path.AddArc($size - $d, $size - $d, $d, $d, 0, 90)
    $path.AddArc(0, $size - $d, $d, $d, 90, 90)
    $path.CloseFigure()

    $bgBrush = New-Object System.Drawing.SolidBrush ([System.Drawing.Color]::FromArgb(255, 155, 246, 178))
    $g.FillPath($bgBrush, $path)

    $font = New-Object System.Drawing.Font 'Segoe UI', ([float]($size * 0.64)), ([System.Drawing.FontStyle]::Bold), ([System.Drawing.GraphicsUnit]::Pixel)
    $fmt = New-Object System.Drawing.StringFormat
    $fmt.Alignment = [System.Drawing.StringAlignment]::Center
    $fmt.LineAlignment = [System.Drawing.StringAlignment]::Center
    $rect = New-Object System.Drawing.RectangleF 0, 0, $size, $size
    $fgBrush = New-Object System.Drawing.SolidBrush ([System.Drawing.Color]::FromArgb(255, 0, 82, 38))
    $g.DrawString('K', $font, $fgBrush, $rect, $fmt)

    $g.Dispose()
    return $bmp
}

function Write-IconDib([System.Drawing.Bitmap]$bmp, [string]$file) {
    $w = $bmp.Width
    $h = $bmp.Height
    $fs = [System.IO.File]::Create($file)
    $bw = New-Object System.IO.BinaryWriter $fs

    $bw.Write([int]40)
    $bw.Write([int]$w)
    $bw.Write([int]($h * 2))
    $bw.Write([int16]1)
    $bw.Write([int16]32)
    $bw.Write([int]0)
    $bw.Write([int]($w * $h * 4))
    $bw.Write([int]0)
    $bw.Write([int]0)
    $bw.Write([int]0)
    $bw.Write([int]0)

    for ($y = $h - 1; $y -ge 0; $y--) {
        for ($x = 0; $x -lt $w; $x++) {
            $c = $bmp.GetPixel($x, $y)
            $bw.Write([byte]$c.B)
            $bw.Write([byte]$c.G)
            $bw.Write([byte]$c.R)
            $bw.Write([byte]$c.A)
        }
    }

    $rowBytes = [int]([Math]::Floor(($w + 31) / 32) * 4)
    for ($y = $h - 1; $y -ge 0; $y--) {
        $bits = New-Object 'byte[]' $rowBytes
        for ($x = 0; $x -lt $w; $x++) {
            if ($bmp.GetPixel($x, $y).A -eq 0) {
                $idx = [int][Math]::Floor($x / 8)
                $bits[$idx] = $bits[$idx] -bor (0x80 -shr ($x % 8))
            }
        }
        $bw.Write($bits)
    }

    $bw.Flush()
    $bw.Dispose()
    $fs.Dispose()
}

$sizes = @(16, 24, 32, 48, 64, 128, 256)
$tmp = Join-Path $outDir 'ico-parts'
New-Item -ItemType Directory -Force -Path $tmp | Out-Null

$parts = @()
foreach ($s in $sizes) {
    $bmp = New-IconBitmap $s
    $png = Join-Path $outDir ("icon-$s.png")
    $bmp.Save($png, [System.Drawing.Imaging.ImageFormat]::Png)
    $bin = Join-Path $tmp ("part-$s.bin")
    Write-IconDib $bmp $bin
    $bmp.Dispose()
    $parts += [pscustomobject]@{ Size = $s; Path = $bin; Length = (Get-Item $bin).Length }
}

$ico = Join-Path $outDir 'icon.ico'
$fs = [System.IO.File]::Create($ico)
$bw = New-Object System.IO.BinaryWriter $fs
$bw.Write([int16]0)
$bw.Write([int16]1)
$bw.Write([int16]$parts.Count)

$offset = 6 + (16 * $parts.Count)
foreach ($p in $parts) {
    $b = if ($p.Size -ge 256) { 0 } else { $p.Size }
    $bw.Write([byte]$b)
    $bw.Write([byte]$b)
    $bw.Write([byte]0)
    $bw.Write([byte]0)
    $bw.Write([int16]1)
    $bw.Write([int16]32)
    $bw.Write([int]$p.Length)
    $bw.Write([int]$offset)
    $offset += $p.Length
}
$bw.Flush()

foreach ($p in $parts) {
    $bytes = [System.IO.File]::ReadAllBytes($p.Path)
    $fs.Write($bytes, 0, $bytes.Length)
}

$fs.Dispose()
Remove-Item $tmp -Recurse -Force
"icon.ico: $((Get-Item $ico).Length) bytes, $($parts.Count) sizes"
