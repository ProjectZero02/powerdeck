#Requires -RunAsAdministrator
<#
  Builds PowerDeck from this checkout and installs it, for use until GitHub releases exist.
  Run from an elevated PowerShell in the repo's helper\ folder:
      powershell -ExecutionPolicy Bypass -File .\scripts\install-from-source.ps1
  Re-running upgrades in place. Remove with: & "$env:ProgramFiles\PowerDeck\PowerDeck.exe" uninstall
#>
$ErrorActionPreference = 'Stop'
Set-Location (Split-Path $PSScriptRoot -Parent)
$version = '2.0.0'

if (-not (Get-Command go -ErrorAction SilentlyContinue)) {
    Write-Host '== Installing Go (winget)'
    winget install --id GoLang.Go -e --accept-package-agreements --accept-source-agreements --silent
    $env:Path = [Environment]::GetEnvironmentVariable('Path', 'Machine') + ';' + [Environment]::GetEnvironmentVariable('Path', 'User')
}
go version

Write-Host '== Building PowerDeck.exe'
go install github.com/tc-hib/go-winres@latest
& "$(go env GOPATH)\bin\go-winres.exe" make --in winres/winres.json --out cmd/powerdeck/rsrc --arch amd64 --product-version $version --file-version "$version.0"
New-Item -ItemType Directory -Force dist | Out-Null
go build -trimpath -ldflags "-s -w -H=windowsgui -X github.com/projectzero02/powerdeck/helper/internal/version.Version=$version" -o dist\PowerDeck.exe .\cmd\powerdeck
if ($LASTEXITCODE) { throw 'build failed' }

Write-Host '== Installing'
$dir = Join-Path $env:ProgramFiles 'PowerDeck'
if (Get-Service PowerDeck -ErrorAction SilentlyContinue) { Stop-Service PowerDeck -Force }
New-Item -ItemType Directory -Force $dir | Out-Null
Copy-Item -Force dist\PowerDeck.exe "$dir\PowerDeck.exe"
$p = Start-Process "$dir\PowerDeck.exe" -ArgumentList 'install' -Wait -PassThru
Get-Content "$env:TEMP\PowerDeck-install.log" -ErrorAction SilentlyContinue
if ($p.ExitCode) { throw "PowerDeck install exit $($p.ExitCode)" }

$lnk = Join-Path $env:ProgramData 'Microsoft\Windows\Start Menu\Programs\PowerDeck.lnk'
$s = (New-Object -ComObject WScript.Shell).CreateShortcut($lnk)
$s.TargetPath = "$dir\PowerDeck.exe"; $s.Arguments = 'open'; $s.Save()

Write-Host '== Checks'
Get-Service PowerDeck | Format-Table Name, Status, StartType
"Phone API port 47321 listening: $(Test-NetConnection 127.0.0.1 -Port 47321 -InformationLevel Quiet)"
Start-Process "$dir\PowerDeck.exe" -ArgumentList 'open'
Write-Host 'Done. The PowerDeck window shows the pairing QR code.'
