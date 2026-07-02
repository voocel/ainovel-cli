'use strict';

/**
 * Dọn cổng dev (backend 8766, Vite 5173) trước khi npm run dev.
 * Tránh backend/Vite cũ chiếm cổng khiến code mới không được nạp.
 */

const { execSync } = require('node:child_process');

const PORTS = [8766, 5173];

function pidsOnPortWin(port) {
  const pids = new Set();
  try {
    const out = execSync(`netstat -ano -p tcp | findstr :${port}`, {
      encoding: 'utf8',
      stdio: ['pipe', 'pipe', 'ignore'],
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
    // Không có process nào trên cổng
  }
  return [...pids];
}

function pidsOnPortUnix(port) {
  try {
    const out = execSync(`lsof -ti tcp:${port} -sTCP:LISTEN`, {
      encoding: 'utf8',
      stdio: ['pipe', 'pipe', 'ignore'],
    }).trim();
    if (!out) return [];
    return out.split(/\s+/).filter((pid) => /^\d+$/.test(pid));
  } catch {
    return [];
  }
}

function killPid(pid) {
  try {
    if (process.platform === 'win32') {
      execSync(`taskkill /F /PID ${pid}`, { stdio: 'ignore' });
    } else {
      process.kill(Number(pid), 'SIGKILL');
    }
    return true;
  } catch {
    return false;
  }
}

function cleanPort(port) {
  const pids = process.platform === 'win32' ? pidsOnPortWin(port) : pidsOnPortUnix(port);
  for (const pid of pids) {
    if (killPid(pid)) {
      console.log(`[dev:clean] Đã dừng PID ${pid} (cổng ${port})`);
    }
  }
}

delete process.env.ELECTRON_RUN_AS_NODE;

console.log('[dev:clean] Dọn cổng dev...');
for (const port of PORTS) {
  cleanPort(port);
}
console.log('[dev:clean] Xong.');
