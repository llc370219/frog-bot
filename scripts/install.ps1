#Requires -Version 5.1
<#
.SYNOPSIS
  frog Windows 一键安装 / 升级脚本（安装 frog.exe 与 frogbot.exe 到用户 PATH）。
.EXAMPLE
  irm https://raw.githubusercontent.com/llc370219/frog-bot/main/scripts/install.ps1 | iex
  $env:CHANNEL="stable"; irm https://raw.githubusercontent.com/llc370219/frog-bot/main/scripts/install.ps1 | iex
#>

$ErrorActionPreference = "Stop"
[Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12

$Repo = "llc370219/frog-bot"
$Channel = if ($env:CHANNEL) { $env:CHANNEL.ToLower() } else { "beta" }
$InstallDir = if ($env:INSTALL_DIR) { $env:INSTALL_DIR } else { Join-Path $env:LOCALAPPDATA "frog\bin" }

function Get-LatestTag {
  if ($env:VERSION) { return $env:VERSION.Trim() }
  if ($Channel -eq "stable") {
    $rel = Invoke-RestMethod -Uri "https://api.github.com/repos/$Repo/releases/latest" -Headers @{ Accept = "application/vnd.github+json" }
    return $rel.tag_name
  }
  $rels = Invoke-RestMethod -Uri "https://api.github.com/repos/$Repo/releases?per_page=20" -Headers @{ Accept = "application/vnd.github+json" }
  foreach ($r in $rels) {
    if (-not $r.draft -and $r.tag_name -match '^v[0-9]') {
      return $r.tag_name
    }
  }
  throw "未找到可用的发布版本"
}

$Tag = Get-LatestTag
$ZipName = "frogbot_${Tag}_windows_amd64.zip"
$BaseUrl = "https://github.com/$Repo/releases/download/$Tag"

Write-Host "==> 正在安装 frog CLI $Tag (windows/amd64)..." -ForegroundColor Green

$TmpDir = Join-Path ([IO.Path]::GetTempPath()) ("frog-install-" + [Guid]::NewGuid().ToString("N"))
New-Item -ItemType Directory -Path $TmpDir -Force | Out-Null

try {
  $ZipPath = Join-Path $TmpDir $ZipName
  $SumPath = Join-Path $TmpDir "SHA256SUMS"

  Invoke-WebRequest -Uri "$BaseUrl/$ZipName" -OutFile $ZipPath -UseBasicParsing
  Invoke-WebRequest -Uri "$BaseUrl/SHA256SUMS" -OutFile $SumPath -UseBasicParsing

  $WantLine = Get-Content $SumPath | Where-Object { $_ -match ("\s+\*?" + [Regex]::Escape($ZipName) + "$") } | Select-Object -First 1
  if (-not $WantLine) { throw "SHA256SUMS 中未找到 $ZipName" }
  $WantHash = ($WantLine -split '\s+')[0].ToLower()
  $GotHash = (Get-FileHash -Path $ZipPath -Algorithm SHA256).Hash.ToLower()
  if ($GotHash -ne $WantHash) { throw "SHA256 校验失败，已中止安装" }

  Expand-Archive -Path $ZipPath -DestinationPath $TmpDir -Force
  $SrcExe = Join-Path $TmpDir ("frogbot_${Tag}_windows_amd64\frogbot.exe")
  if (-not (Test-Path $SrcExe)) { throw "安装包中未找到 frogbot.exe" }

  New-Item -ItemType Directory -Path $InstallDir -Force | Out-Null
  $FrogExe = Join-Path $InstallDir "frog.exe"
  $FrogbotExe = Join-Path $InstallDir "frogbot.exe"

  # 若旧版 frog 正在后台运行，先优雅停止
  if (Test-Path $FrogExe) {
    try { & $FrogExe stop 2>$null | Out-Null } catch {}
  } elseif (Test-Path $FrogbotExe) {
    try { & $FrogbotExe stop 2>$null | Out-Null } catch {}
  }

  Copy-Item -Path $SrcExe -Destination $FrogExe -Force
  Copy-Item -Path $SrcExe -Destination $FrogbotExe -Force

  $UserPath = [Environment]::GetEnvironmentVariable("Path", "User")
  $Parts = @()
  if ($UserPath) { $Parts = $UserPath -split ';' | Where-Object { $_ -ne '' } }
  if (-not ($Parts | Where-Object { $_.TrimEnd('\') -ieq $InstallDir.TrimEnd('\') })) {
    $NewPath = ($Parts + $InstallDir) -join ';'
    [Environment]::SetEnvironmentVariable("Path", $NewPath, "User")
    $env:Path = "$InstallDir;$env:Path"
    Write-Host "==> 已将 $InstallDir 加入用户 PATH（重新打开终端即可直接使用 frog）" -ForegroundColor Yellow
  }

  Write-Host ""
  Write-Host "  🐸 frog CLI $Tag 已安装完成！" -ForegroundColor Green
  Write-Host "  安装位置：$FrogExe"
  Write-Host ""
  Write-Host "  常用指令（在任意 PowerShell / CMD 中直接输入）："
  Write-Host "    frog                   打开交互式状态面板与快捷菜单"
  Write-Host "    frog start             后台静默启动（关闭终端不退出）"
  Write-Host "    frog pair              终端直接出二维码给手机扫码"
  Write-Host "    frog status            查看运行与远程穿透状态"
  Write-Host "    frog logs -f           查看实时日志"
  Write-Host "    frog service install   设置开机登录后自动后台运行"
  Write-Host "    frog update            一键升级最新版本"
  Write-Host ""
} finally {
  Remove-Item -Path $TmpDir -Recurse -Force -ErrorAction SilentlyContinue
}
