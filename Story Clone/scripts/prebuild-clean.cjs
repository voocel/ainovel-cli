'use strict';

/**
 * Dọn process/file lock trước PyInstaller build.
 * Tránh WinError 5 khi xóa backend/dist/run_backend.
 */

const fs = require('node:fs');
const path = require('node:path');
const { execSync, spawnSync } = require('node:child_process');

const ROOT = path.resolve(__dirname, '..');
const BACKEND = path.join(ROOT, 'backend');
const PYI_DIST = path.join(BACKEND, 'dist', '_pyi_out');
const PYI_WORK = path.join(BACKEND, 'build', '_pyi_work');
const LEGACY_DIST = path.join(BACKEND, 'dist', 'run_backend');
const PORTS = [8766];

function runQuiet(cmd) {
  try {
    execSync(cmd, { stdio: 'ignore', windowsHide: true });
    return true;
  } catch {
    return false;
  }
}

function pidsOnPortWin(port) {
  const pids = new Set();
  try {
    const out = execSync(`netstat -ano -p tcp | findstr :${port}`, {
      encoding: 'utf8',
      stdio: ['pipe', 'pipe', 'ignore'],
      windowsHide: true,
    });
    for (const line of out.split(/\r?\n/)) {
      const trimmed = line.trim();
      if (!trimmed.includes('LISTENING')) continue;
      const local = trimmed.split(/\s+/)[1] || '';
      if (!local.endsWith(`:${port}`)) continue;
      const pid = trimmed.split(/\s+/).pop();
      if (/^\d+$/.test(pid) && pid !== '0') pids.add(pid);
    }
  } catch {
    // no listeners
  }
  return [...pids];
}

function killPid(pid) {
  if (process.platform === 'win32') {
    return runQuiet(`taskkill /F /PID ${pid}`);
  }
  try {
    process.kill(Number(pid), 'SIGKILL');
    return true;
  } catch {
    return false;
  }
}

function killImage(imageName) {
  if (process.platform !== 'win32') return;
  runQuiet(`taskkill /F /IM ${imageName}`);
}

function sleep(ms) {
  spawnSync('powershell', ['-NoProfile', '-Command', `Start-Sleep -Milliseconds ${ms}`], {
    stdio: 'ignore',
    windowsHide: true,
  });
}

function removeDir(dir) {
  if (!fs.existsSync(dir)) return true;
  try {
    fs.rmSync(dir, { recursive: true, force: true, maxRetries: 5, retryDelay: 400 });
    return true;
  } catch (err) {
    console.warn(`[prebuild:clean] Không xóa được ${dir}: ${err.message}`);
    return false;
  }
}

delete process.env.ELECTRON_RUN_AS_NODE;

console.log('[prebuild:clean] Dừng process có thể khóa backend bundle...');

for (const port of PORTS) {
  for (const pid of pidsOnPortWin(port)) {
    if (killPid(pid)) console.log(`[prebuild:clean] Đã dừng PID ${pid} (cổng ${port})`);
  }
}

if (process.platform === 'win32') {
  for (const image of ['run_backend.exe', 'Story Clone.exe']) {
    killImage(image);
  }
}

sleep(1500);

console.log('[prebuild:clean] Xóa thư mục PyInstaller tạm...');
removeDir(PYI_DIST);
removeDir(PYI_WORK);

// Thư mục legacy có thể bị khóa nếu app đang chạy — không bắt buộc xóa.
if (fs.existsSync(LEGACY_DIST)) {
  console.log('[prebuild:clean] Giữ nguyên backend/dist/run_backend (legacy) nếu đang bị khóa.');
}

console.log('[prebuild:clean] Xong.');
