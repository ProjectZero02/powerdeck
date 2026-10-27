# PowerDeck helper installer for Windows.
#
#   irm https://github.com/ProjectZero02/powerdeck/releases/latest/download/install.ps1 | iex
#
# It downloads PowerDeck-Setup.exe from the latest release, checks it against the release's
# SHA256SUMS.txt, installs it silently (asking for admin rights once) and opens the PowerDeck
# window with the QR code that adds this PC to the app. Re-running it upgrades in place.
# The installed app additionally checks an Ed25519 signature on every automatic update.
$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
[Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12

$repo = 'ProjectZero02/powerdeck'
$base = if ($env:POWERDECK_VERSION) { "https://github.com/$repo/releases/download/v$($env:POWERDECK_VERSION.TrimStart('v'))" } else { "https://github.com/$repo/releases/latest/download" }
$dir = Join-Path $env:TEMP ("PowerDeck-" + [guid]::NewGuid())
New-Item -ItemType Directory $dir | Out-Null
try {
    Write-Host 'Downloading PowerDeck...'
    $setup = Join-Path $dir 'PowerDeck-Setup.exe'
    Invoke-WebRequest "$base/PowerDeck-Setup.exe" -OutFile $setup -UseBasicParsing
    $sums = (Invoke-WebRequest "$base/SHA256SUMS.txt" -UseBasicParsing).Content
    if ($sums -is [byte[]]) { $sums = [Text.Encoding]::UTF8.GetString($sums) }
    $line = ($sums -split "`n") | Where-Object { $_ -match '\sPowerDeck-Setup\.exe\s*$' } | Select-Object -First 1
    if (-not $line) { throw 'PowerDeck-Setup.exe is not listed in SHA256SUMS.txt' }
    $want = ($line -split '\s+')[0].ToLower()
    $got = (Get-FileHash $setup -Algorithm SHA256).Hash.ToLower()
    if ($got -ne $want) { throw "Checksum mismatch (got $got, want $want). Not installing." }
    Write-Host 'Checksum OK. Installing (approve the admin prompt)...'
    $p = Start-Process $setup -ArgumentList '/VERYSILENT', '/SUPPRESSMSGBOXES', '/NORESTART', '/SP-' -Verb RunAs -Wait -PassThru
    if ($p.ExitCode -ne 0) { throw "Setup exited with code $($p.ExitCode). Log: $env:TEMP\PowerDeck-install.log" }
    $exe = Join-Path $env:ProgramFiles 'PowerDeck\PowerDeck.exe'
    Write-Host 'PowerDeck is installed. Opening the PowerDeck window...'
    Start-Process $exe -ArgumentList 'open'
}
finally {
    Remove-Item $dir -Recurse -Force -ErrorAction SilentlyContinue
}
