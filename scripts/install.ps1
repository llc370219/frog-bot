# frog CLI Windows installer / upgrader
# Usage:
#   irm https://raw.githubusercontent.com/llc370219/frog-bot/main/scripts/install.ps1 | iex
#   $env:CHANNEL="stable"; irm https://raw.githubusercontent.com/llc370219/frog-bot/main/scripts/install.ps1 | iex

$ErrorActionPreference = "Stop"
[Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12

$Repo = "llc370219/frog-bot"
$Channel = if ($env:CHANNEL) { $env:CHANNEL.ToLower() } else { "beta" }
$InstallDir = if ($env:INSTALL_DIR) { $env:INSTALL_DIR } else { Join-Path $env:LOCALAPPDATA "frog\bin" }

function Convert-TagSortKey([string]$tag) {
  if ($tag -notmatch '^v(\d+)\.(\d+)\.(\d+)(?:-(.+))?$') { return "" }
  $maj = $Matches[1].PadLeft(8, '0')
  $min = $Matches[2].PadLeft(8, '0')
  $pat = $Matches[3].PadLeft(8, '0')
  if (-not $Matches[4]) {
    return "$maj.$min.$pat.1"
  }
  $preParts = ($Matches[4] -split '\.') | ForEach-Object {
    if ($_ -match '^\d+$') { $_.PadLeft(8, '0') } else { $_ }
  }
  return "$maj.$min.$pat.0." + ($preParts -join '.')
}

function Get-FrogLatestTag {
  if ($env:VERSION) { return $env:VERSION.Trim() }
  $headers = @{ Accept = "application/vnd.github+json"; "User-Agent" = "frog-installer" }
  if ($Channel -eq "stable") {
    $rel = Invoke-RestMethod -Uri "https://api.github.com/repos/$Repo/releases/latest" -Headers $headers
    return $rel.tag_name
  }
  $rels = Invoke-RestMethod -Uri "https://api.github.com/repos/$Repo/releases?per_page=30" -Headers $headers
  $bestTag = ""
  $bestKey = ""
  foreach ($r in $rels) {
    if ($r.draft) { continue }
    $k = Convert-TagSortKey $r.tag_name
    if ($k -and ([string]::CompareOrdinal($k, $bestKey) -gt 0)) {
      $bestKey = $k
      $bestTag = $r.tag_name
    }
  }
  if ($bestTag) { return $bestTag }
  throw "No published release found on GitHub"
}

$Tag = Get-FrogLatestTag
$ZipName = "frogbot_${Tag}_windows_amd64.zip"
$BaseUrl = "https://github.com/$Repo/releases/download/$Tag"

Write-Host "==> Installing frog CLI $Tag (windows/amd64)..." -ForegroundColor Green

$TmpDir = Join-Path ([IO.Path]::GetTempPath()) ("frog-install-" + [Guid]::NewGuid().ToString("N"))
New-Item -ItemType Directory -Path $TmpDir -Force | Out-Null

try {
  $ZipPath = Join-Path $TmpDir $ZipName
  $SumPath = Join-Path $TmpDir "SHA256SUMS"

  Invoke-WebRequest -Uri "$BaseUrl/$ZipName" -OutFile $ZipPath -UseBasicParsing
  Invoke-WebRequest -Uri "$BaseUrl/SHA256SUMS" -OutFile $SumPath -UseBasicParsing

  $WantLine = Get-Content $SumPath | Where-Object { $_ -match ("\s+\*?" + [Regex]::Escape($ZipName) + "$") } | Select-Object -First 1
  if (-not $WantLine) { throw "SHA256SUMS does not contain $ZipName" }
  $WantHash = ($WantLine -split '\s+')[0].ToLower()
  $GotHash = (Get-FileHash -Path $ZipPath -Algorithm SHA256).Hash.ToLower()
  if ($GotHash -ne $WantHash) { throw "SHA256 verification failed, aborting install" }

  Expand-Archive -Path $ZipPath -DestinationPath $TmpDir -Force
  $SrcExe = Join-Path $TmpDir ("frogbot_${Tag}_windows_amd64\frogbot.exe")
  if (-not (Test-Path $SrcExe)) { throw "frogbot.exe not found inside archive" }

  New-Item -ItemType Directory -Path $InstallDir -Force | Out-Null
  $FrogExe = Join-Path $InstallDir "frog.exe"
  $FrogbotExe = Join-Path $InstallDir "frogbot.exe"

  # Stop any running old frog/frogbot processes from InstallDir without blocking on legacy binaries
  Get-Process -ErrorAction SilentlyContinue | Where-Object {
    try { $_.Path -ieq $FrogExe -or $_.Path -ieq $FrogbotExe } catch { $false }
  } | Stop-Process -Force -ErrorAction SilentlyContinue
  Start-Sleep -Milliseconds 300

  Copy-Item -Path $SrcExe -Destination $FrogExe -Force
  Copy-Item -Path $SrcExe -Destination $FrogbotExe -Force

  $UserPath = [Environment]::GetEnvironmentVariable("Path", "User")
  $Parts = @()
  if ($UserPath) { $Parts = $UserPath -split ';' | Where-Object { $_ -ne '' } }
  if (-not ($Parts | Where-Object { $_.TrimEnd('\') -ieq $InstallDir.TrimEnd('\') })) {
    $NewPath = ($Parts + $InstallDir) -join ';'
    [Environment]::SetEnvironmentVariable("Path", $NewPath, "User")
    Write-Host "==> Added $InstallDir to User PATH" -ForegroundColor Yellow
  }
  if (-not (($env:Path -split ';') | Where-Object { $_.TrimEnd('\') -ieq $InstallDir.TrimEnd('\') })) {
    $env:Path = "$InstallDir;$env:Path"
  }

  Write-Host "==> Installed: $FrogExe" -ForegroundColor Green
  & $FrogExe help
} finally {
  Remove-Item -Path $TmpDir -Recurse -Force -ErrorAction SilentlyContinue
}
