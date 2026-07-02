<#
.SYNOPSIS
  Thiết lập môi trường dev và/hoặc đóng gói Story Clone thành bản Windows portable.

.DESCRIPTION
  Script dùng cho dự án Story Clone (Electron + React + FastAPI):

  Chế độ DEV (mặc định):
    - Tạo backend\.venv, cài requirements.txt
    - npm install, typecheck
    - Tạo run-dev.ps1, run-backend.ps1

  Chế độ ĐÓNG GÓI (-Package):
    - Cài thêm PyInstaller (requirements-dev.txt)
    - Build backend.exe (PyInstaller onedir, không cần Python trên máy đích)
    - Build renderer + Electron (Vite, bytenode)
    - Chạy electron-builder → dist-packaged\
    - Bản cài đặt .exe có icon.ico, shortcut Desktop (NSIS)
    - Thư mục portable: dist-packaged\win-unpacked\ (copy sang máy khác chạy trực tiếp)

  Yêu cầu máy BUILD: Python 3.11+, Node.js LTS, Windows x64.

.PARAMETER SkipPythonInstall
  Bỏ qua tạo .venv và pip install.

.PARAMETER SkipNodeInstall
  Bỏ qua npm install.

.PARAMETER RecreateVenv
  Xóa backend\.venv và tạo lại.

.PARAMETER Build
  Chỉ build renderer/electron (npm run build), không đóng gói installer.

.PARAMETER Package
  Đóng gói đầy đủ: backend bundle + installer .exe + win-unpacked portable.

.PARAMETER DirOnly
  Dùng cùng -Package: chỉ tạo win-unpacked, không tạo file Setup .exe.

.PARAMETER DesktopShortcut
  Sau khi -Package, tạo shortcut Desktop trên máy hiện tại trỏ tới bản portable.

.EXAMPLE
  .\setup-portable.ps1
  # Lần đầu: cài dev dependencies

.EXAMPLE
  .\setup-portable.ps1 -Package
  # Tạo installer + portable (mang sang máy khác cài hoặc chạy win-unpacked)

.EXAMPLE
  .\setup-portable.ps1 -Package -DirOnly -DesktopShortcut
  # Chỉ portable folder + shortcut Desktop
#>

param(
    [switch]$SkipPythonInstall,
    [switch]$SkipNodeInstall,
    [switch]$RecreateVenv,
    [switch]$Build,
    [switch]$Package,
    [switch]$DirOnly,
    [switch]$DesktopShortcut
)

$ErrorActionPreference = "Stop"
Set-StrictMode -Version Latest

$Root = Split-Path -Parent $MyInvocation.MyCommand.Path
Set-Location $Root

$BackendDir       = Join-Path $Root "backend"
$RendererDir      = Join-Path $Root "renderer"
$ElectronDir      = Join-Path $Root "electron"
$RequirementsFile = Join-Path $BackendDir "requirements.txt"
$RequirementsDev  = Join-Path $BackendDir "requirements-dev.txt"
$VenvDir          = Join-Path $BackendDir ".venv"
$VenvPython       = Join-Path $VenvDir "Scripts\python.exe"
$VenvPip          = Join-Path $VenvDir "Scripts\pip.exe"
$DataDir          = Join-Path $BackendDir "data"
$ScriptsDir       = Join-Path $Root "scripts"
$IconIco          = Join-Path $Root "icon.ico"
$IconPng          = Join-Path $Root "icon.png"
$DistPackaged     = Join-Path $Root "dist-packaged"
$WinUnpacked      = Join-Path $DistPackaged "win-unpacked"
$PortableExe      = Join-Path $WinUnpacked "Story Clone.exe"
$BuildScript      = Join-Path $ScriptsDir "build-encrypted.cjs"

function Write-Step {
    param([string]$Message)
    Write-Host ""
    Write-Host "==> $Message" -ForegroundColor Cyan
}

function Assert-Path {
    param([string]$Path, [string]$Label)
    if (-not (Test-Path -LiteralPath $Path)) {
        throw "Không tìm thấy $Label tại: $Path"
    }
}

function Get-CommandOrThrow {
    param([string]$Name, [string]$InstallHint)
    $cmd = Get-Command $Name -ErrorAction SilentlyContinue
    if (-not $cmd) { throw "Không tìm thấy '$Name'. $InstallHint" }
    return $cmd.Source
}

function Write-Utf8NoBom {
    param([string]$Path, [string]$Content)
    $utf8NoBom = New-Object System.Text.UTF8Encoding($false)
    [System.IO.File]::WriteAllText($Path, $Content, $utf8NoBom)
}

function New-StoryCloneDesktopShortcut {
    param(
        [string]$TargetExe,
        [string]$WorkingDir,
        [string]$IconFile
    )
    $desktop = [Environment]::GetFolderPath("Desktop")
    $lnkPath = Join-Path $desktop "Story Clone.lnk"
    $shell = New-Object -ComObject WScript.Shell
    $shortcut = $shell.CreateShortcut($lnkPath)
    $shortcut.TargetPath = $TargetExe
    $shortcut.WorkingDirectory = $WorkingDir
    if (Test-Path -LiteralPath $IconFile) {
        $shortcut.IconLocation = "$IconFile,0"
    }
    $shortcut.Description = "Story Clone - Phần mềm tạo tiểu thuyết bằng AI (1TouchPro)"
    $shortcut.Save()
    [System.Runtime.InteropServices.Marshal]::ReleaseComObject($shell) | Out-Null
    return $lnkPath
}

function Write-PortableReadme {
    param([string]$OutDir, [bool]$HasInstaller)
    $installerNote = if ($HasInstaller) {
        @"
CÁCH 1 — Cài đặt (khuyến nghị cho người dùng phổ thông)
  1. Chạy file: Story Clone Setup *.exe
  2. Chọn thư mục cài đặt → Next → Install
  3. Shortcut Desktop và Start Menu được tạo tự động (có icon)
  4. Máy đích KHÔNG cần cài Python hay Node.js

"@
    } else { "" }

    $text = @"
STORY CLONE — HƯỚNG DẪN MANG SANG MÁY KHÁC
==========================================

$installerNote
CÁCH 2 — Portable (không cài, chạy trực tiếp)
  1. Copy cả thư mục: win-unpacked\
  2. Trên máy đích, chạy: win-unpacked\Story Clone.exe
  3. Hoặc chạy: win-unpacked\Tao-Shortcut-Desktop.ps1 để tạo icon Desktop

Lưu ý:
  - Windows 10/11 x64
  - Không cần Python / Node.js (backend đã gói sẵn trong resources\)
  - Dữ liệu SQLite: backend\data\ (tạo khi chạy lần đầu)
  - Cổng backend nội bộ: 127.0.0.1:8766 (tự khởi động cùng app)

Phiên bản build: $(Get-Date -Format "yyyy-MM-dd HH:mm")
"@
    Write-Utf8NoBom -Path (Join-Path $OutDir "HUONG-DAN-MAY-KHAC.txt") -Content $text
}

function Write-PortableShortcutScript {
    param([string]$WinDir)
    $scriptPath = Join-Path $WinDir "Tao-Shortcut-Desktop.ps1"
    $content = @'
$ErrorActionPreference = "Stop"
$here = Split-Path -Parent $MyInvocation.MyCommand.Path
$exe = Join-Path $here "Story Clone.exe"
$icon = Join-Path $here "resources\app\icon.ico"
if (-not (Test-Path $exe)) { throw "Không tìm thấy Story Clone.exe" }
$desktop = [Environment]::GetFolderPath("Desktop")
$lnk = Join-Path $desktop "Story Clone.lnk"
$sh = New-Object -ComObject WScript.Shell
$sc = $sh.CreateShortcut($lnk)
$sc.TargetPath = $exe
$sc.WorkingDirectory = $here
if (Test-Path $icon) { $sc.IconLocation = "$icon,0" }
$sc.Description = "Story Clone"
$sc.Save()
Write-Host "Đã tạo shortcut: $lnk" -ForegroundColor Green
'@
    Write-Utf8NoBom -Path $scriptPath -Content $content
}

Write-Host "=======================================================" -ForegroundColor DarkGray
Write-Host " STORY CLONE — THIẾT LẬP / ĐÓNG GÓI PORTABLE" -ForegroundColor Green
Write-Host " Thư mục: $Root"
if ($Package) {
    Write-Host " Chế độ: ĐÓNG GÓI $(if ($DirOnly) { '(chỉ win-unpacked)' } else { '(installer + portable)' })" -ForegroundColor Yellow
}
Write-Host "=======================================================" -ForegroundColor DarkGray

Assert-Path -Path $BackendDir -Label "backend"
Assert-Path -Path $RendererDir -Label "renderer"
Assert-Path -Path $ElectronDir -Label "electron"
Assert-Path -Path $RequirementsFile -Label "requirements.txt"
Assert-Path -Path (Join-Path $Root "package.json") -Label "package.json"

if ($Package) {
    Assert-Path -Path $IconIco -Label "icon.ico (bắt buộc cho exe và shortcut)"
    Assert-Path -Path $BuildScript -Label "scripts/build-encrypted.cjs"
}

if (-not (Test-Path -LiteralPath $DataDir)) {
    New-Item -ItemType Directory -Force -Path $DataDir | Out-Null
}

$pythonCmd = $null
$npmCmd = $null
$pipRequirements = if ($Package) { $RequirementsDev } else { $RequirementsFile }

if (-not $SkipPythonInstall) {
    Write-Step "Kiểm tra Python 3.11+"
    $pythonCmd = Get-CommandOrThrow -Name "python" -InstallHint "Cài Python 3.11+ từ python.org (Add to PATH)."
    & $pythonCmd --version

    if ($RecreateVenv -and (Test-Path -LiteralPath $VenvDir)) {
        Write-Step "Xóa .venv cũ"
        Remove-Item -LiteralPath $VenvDir -Recurse -Force
    }

    if (-not (Test-Path -LiteralPath $VenvPython)) {
        Write-Step "Tạo virtualenv: backend\.venv"
        & $pythonCmd -m venv $VenvDir
    }

    Write-Step "Cài pip packages: $(Split-Path -Leaf $pipRequirements)"
    & $VenvPython -m pip install --upgrade pip
    & $VenvPython -m pip install -r $pipRequirements

    Write-Step "Kiểm tra import FastAPI"
    Push-Location $BackendDir
    try {
        & $VenvPython -c "from app.main import app; print('OK:', app.title)"
    } finally {
        Pop-Location
    }

    if ($Package) {
        $pyinstaller = Join-Path $VenvDir "Scripts\pyinstaller.exe"
        if (-not (Test-Path -LiteralPath $pyinstaller)) {
            throw "PyInstaller chưa được cài. Kiểm tra requirements-dev.txt"
        }
        Write-Host "PyInstaller: $pyinstaller" -ForegroundColor DarkGray
    }
} else {
    Write-Host "Bỏ qua Python (-SkipPythonInstall)" -ForegroundColor Yellow
}

if (-not $SkipNodeInstall) {
    Write-Step "Kiểm tra Node.js và npm"
    $nodeCmd = Get-CommandOrThrow -Name "node" -InstallHint "Cài Node.js LTS từ nodejs.org."
    $npmCmd = Get-Command "npm.cmd" -ErrorAction SilentlyContinue
    if (-not $npmCmd) { $npmCmd = Get-Command "npm" -ErrorAction SilentlyContinue }
    if (-not $npmCmd) { throw "Không tìm thấy npm." }
    & $nodeCmd --version
    & $npmCmd.Source --version

    Write-Step "npm install"
    & $npmCmd.Source install --prefix $Root

    if (-not $Package) {
        Write-Step "TypeScript check"
        & $npmCmd.Source run typecheck --prefix $Root
    }
} else {
    Write-Host "Bỏ qua Node (-SkipNodeInstall)" -ForegroundColor Yellow
}

if ($Build -and -not $Package) {
    if (-not $npmCmd) {
        $npmCmd = Get-Command "npm.cmd" -ErrorAction SilentlyContinue
        if (-not $npmCmd) { $npmCmd = Get-Command "npm" -ErrorAction SilentlyContinue }
        if (-not $npmCmd) { throw "Không tìm thấy npm để build." }
    }
    Write-Step "npm run build"
    & $npmCmd.Source run build --prefix $Root
}

Write-Step "Tạo script dev tiện dụng"
$RunDevPath = Join-Path $Root "run-dev.ps1"
$RunBackendPath = Join-Path $Root "run-backend.ps1"

Write-Utf8NoBom -Path $RunDevPath -Content @"
`$ErrorActionPreference = "Stop"
`$Root = Split-Path -Parent `$MyInvocation.MyCommand.Path
`$env:ELECTRON_RUN_AS_NODE = `$null
Set-Location `$Root
npm.cmd run dev
"@

Write-Utf8NoBom -Path $RunBackendPath -Content @"
`$ErrorActionPreference = "Stop"
`$Root = Split-Path -Parent `$MyInvocation.MyCommand.Path
`$BackendDir = Join-Path `$Root "backend"
`$Python = Join-Path `$BackendDir ".venv\Scripts\python.exe"
if (-not (Test-Path `$Python)) { `$Python = "python" }
Set-Location `$BackendDir
& `$Python -m uvicorn app.main:app --host 127.0.0.1 --port 8766 --reload --reload-exclude data
"@

if ($Package) {
    Write-Step "Đóng gói Story Clone (backend + Electron + icon)"
    $env:ELECTRON_RUN_AS_NODE = $null
    $nodeCmd = Get-CommandOrThrow -Name "node" -InstallHint "Cần Node.js để chạy build-encrypted.cjs"

    Write-Step "Dọn process khóa backend trước khi build"
    & $nodeCmd (Join-Path $ScriptsDir "prebuild-clean.cjs")
    if ($LASTEXITCODE -ne 0) {
        Write-Host "Cảnh báo: prebuild-clean gặp sự cố — vẫn thử build tiếp." -ForegroundColor Yellow
    }

    $buildArgs = @($BuildScript)
    if ($DirOnly) { $buildArgs += "--dir" }

    & $nodeCmd @buildArgs
    if ($LASTEXITCODE -ne 0) { throw "Build thất bại (exit $LASTEXITCODE)" }

    if (-not (Test-Path -LiteralPath $PortableExe)) {
        throw "Không tìm thấy portable exe: $PortableExe"
    }

    $bundledBackend = Join-Path $WinUnpacked "resources\app\backend\dist\_pyi_out\run_backend\run_backend.exe"
    if (-not (Test-Path -LiteralPath $bundledBackend)) {
        $bundledBackend = Join-Path $WinUnpacked "resources\app\backend\dist\run_backend\run_backend.exe"
    }
    if (-not (Test-Path -LiteralPath $bundledBackend)) {
        Write-Host "Cảnh báo: chưa thấy backend bundle tại $bundledBackend" -ForegroundColor Yellow
    } else {
        Write-Host "Backend bundle: OK" -ForegroundColor DarkGray
    }

    $setupExe = Get-ChildItem -Path $DistPackaged -Filter "Story Clone Setup*.exe" -ErrorAction SilentlyContinue | Select-Object -First 1

    Write-PortableShortcutScript -WinDir $WinUnpacked
    Write-PortableReadme -OutDir $DistPackaged -HasInstaller:([bool]$setupExe)

    if ($DesktopShortcut) {
        Write-Step "Tạo shortcut Desktop (máy build hiện tại)"
        $iconInBundle = Join-Path $WinUnpacked "resources\app\icon.ico"
        $iconUse = if (Test-Path $iconInBundle) { $iconInBundle } else { $IconIco }
        $lnk = New-StoryCloneDesktopShortcut -TargetExe $PortableExe -WorkingDir $WinUnpacked -IconFile $iconUse
        Write-Host "Shortcut: $lnk" -ForegroundColor Green
    }
}

Write-Step "Hoàn tất"
Write-Host ""

if ($Package) {
    Write-Host "=== KẾT QUẢ ĐÓNG GÓI ===" -ForegroundColor Green
    Write-Host "Portable (mang sang máy khác):" -ForegroundColor Yellow
    Write-Host "  $WinUnpacked"
    Write-Host "  Chạy: Story Clone.exe"
    Write-Host ""
    $setupFiles = Get-ChildItem -Path $DistPackaged -Filter "Story Clone Setup*.exe" -ErrorAction SilentlyContinue
    if ($setupFiles) {
        Write-Host "Installer (có shortcut Desktop + icon khi cài):" -ForegroundColor Yellow
        foreach ($f in $setupFiles) { Write-Host "  $($f.FullName)" }
        Write-Host ""
    }
    Write-Host "Hướng dẫn máy khác:" -ForegroundColor Yellow
    Write-Host "  $(Join-Path $DistPackaged 'HUONG-DAN-MAY-KHAC.txt')"
    Write-Host ""
    Write-Host "Trên máy đích (portable): chạy Tao-Shortcut-Desktop.ps1 trong win-unpacked\" -ForegroundColor DarkGray
} else {
    Write-Host "Dev — Backend:  http://127.0.0.1:8766" -ForegroundColor Green
    Write-Host "Dev — API docs: http://127.0.0.1:8766/docs" -ForegroundColor Green
    Write-Host "Dev — Renderer: http://127.0.0.1:5173" -ForegroundColor Green
    Write-Host ""
    Write-Host "Chạy dev:  powershell -ExecutionPolicy Bypass -File `"$RunDevPath`"" -ForegroundColor Yellow
    Write-Host ""
    Write-Host "Đóng gói exe:" -ForegroundColor Yellow
    Write-Host "  powershell -ExecutionPolicy Bypass -File `"$($MyInvocation.MyCommand.Path)`" -Package"
    Write-Host "  powershell -ExecutionPolicy Bypass -File `"$($MyInvocation.MyCommand.Path)`" -Package -DesktopShortcut"
}

Write-Host "=======================================================" -ForegroundColor DarkGray
