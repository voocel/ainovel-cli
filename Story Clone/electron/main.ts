import { app, BrowserWindow, ipcMain, dialog, Notification, Menu } from "electron";
import { spawn, ChildProcessWithoutNullStreams, exec } from "node:child_process";
import fs from "node:fs";
import http from "node:http";
import path from "node:path";
import crypto from "node:crypto";
import os from "node:os";

// ============================================================
// INTERNAL — không đặt tên gợi ý
// ============================================================
const LICENSE_FILE = path.join(app.getPath("userData"), "license.dat");

// Secret được tái tạo tại runtime từ nhiều mảnh — khó grep hơn plain string
function _s(): string {
  const p = ["SC", "2026", "aInOvEl", "x9K#mPqR", "!vLwZtYu"];
  const sep = ["-", "-", "-", ""];
  return p.map((v, i) => v + (sep[i] ?? "")).join("");
}

/** Sinh Machine ID duy nhất từ thông tin máy */
function getMachineId(): string {
  const raw = `${os.hostname()}::${os.platform()}::${os.arch()}::${os.cpus()[0]?.model ?? "cpu"}`;
  return crypto.createHash("sha256").update(raw).digest("hex").slice(0, 16).toUpperCase();
}

/** Định dạng 16 ký tự → XXXX-XXXX-XXXX-XXXX */
function formatKey(hex16: string): string {
  const s = hex16.toUpperCase().slice(0, 16).padEnd(16, "0");
  return `${s.slice(0,4)}-${s.slice(4,8)}-${s.slice(8,12)}-${s.slice(12,16)}`;
}

/** Lấy Machine Code đã định dạng */
function getMachineCode(): string {
  return formatKey(getMachineId());
}

/** Tính Activation Key kỳ vọng từ machine code */
function _calcExpected(machineCode: string): string {
  const normalized = machineCode.replace(/[\s-]/g, "").toUpperCase();
  const digest = crypto.createHmac("sha256", _s())
    .update(normalized)
    .digest("hex");
  return formatKey(digest.slice(0, 16));
}

/** Kiểm tra key nhập vào có hợp lệ không */
function verifyKey(inputKey: string): boolean {
  const machineCode = getMachineCode();
  const expected = _calcExpected(machineCode);
  const normalizeInput = inputKey.replace(/[\s-]/g, "").toUpperCase();
  const normalizeExpected = expected.replace(/[\s-]/g, "").toUpperCase();
  return normalizeInput === normalizeExpected;
}

/** Đọc license đã lưu */
function loadLicense(): boolean {
  try {
    if (!fs.existsSync(LICENSE_FILE)) return false;
    const data = JSON.parse(fs.readFileSync(LICENSE_FILE, "utf-8"));
    // Kiểm tra lại key đã lưu với machine hiện tại
    return verifyKey(data.key ?? "");
  } catch {
    return false;
  }
}

/** Lưu license sau khi kích hoạt thành công */
function saveLicense(key: string): void {
  fs.writeFileSync(LICENSE_FILE, JSON.stringify({ key, activated_at: new Date().toISOString() }), "utf-8");
}

// ============================================================
// BACKEND
// ============================================================
let backend: ChildProcessWithoutNullStreams | null = null;

function backendRoot() {
  return path.join(app.getAppPath(), "backend");
}

function backendPython() {
  const localPython = path.join(backendRoot(), ".venv", "Scripts", "python.exe");
  return fs.existsSync(localPython) ? localPython : "python";
}

const BACKEND_PORT = 8766;
const BACKEND_API_VERSION = 2;

function backendHealthUrl() {
  return `http://127.0.0.1:${BACKEND_PORT}/health`;
}

type ApiRequestInit = { method?: string; body?: string; timeoutMs?: number };

function nodeApiRequest(
  path: string,
  init: ApiRequestInit = {}
): Promise<{ status: number; body: string; contentType: string }> {
  const method = (init.method || "GET").toUpperCase();
  const body = init.body;
  const timeoutMs = init.timeoutMs ?? 30000;
  return new Promise((resolve, reject) => {
    const req = http.request(
      {
        hostname: "127.0.0.1",
        port: BACKEND_PORT,
        path,
        method,
        headers: body
          ? {
              "Content-Type": "application/json",
              "Content-Length": Buffer.byteLength(body),
            }
          : {},
      },
      res => {
        let data = "";
        res.on("data", (chunk: Buffer) => {
          data += chunk.toString("utf8");
        });
        res.on("end", () => {
          resolve({
            status: res.statusCode ?? 500,
            body: data,
            contentType: String(res.headers["content-type"] ?? ""),
          });
        });
      }
    );
    req.on("error", reject);
    req.setTimeout(timeoutMs, () => {
      req.destroy(new Error(`timeout after ${timeoutMs}ms`));
    });
    if (body) req.write(body);
    req.end();
  });
}

function apiTimeoutMs(path: string, method?: string): number {
  const m = (method || "GET").toUpperCase();
  if (m === "POST" && (path.includes("/start") || path.includes("/resume") || path.includes("/artist-prompts/regenerate"))) {
    return 600000;
  }
  if (path === "/health") return 8000;
  return 60000;
}

function killBackendPort(): Promise<void> {
  return new Promise(resolve => {
    if (process.platform !== "win32") return resolve();
    const cmd = `Get-NetTCPConnection -LocalPort ${BACKEND_PORT} -State Listen -ErrorAction SilentlyContinue | ForEach-Object { Stop-Process -Id $_.OwningProcess -Force -ErrorAction SilentlyContinue }`;
    exec(`powershell -NoProfile -Command "${cmd}"`, () => resolve());
  });
}

function backendApiVersion(timeoutMs = 1200): Promise<number> {
  return new Promise(resolve => {
    const req = http.get(backendHealthUrl(), res => {
      let body = "";
      res.on("data", (chunk: Buffer) => { body += chunk.toString("utf8"); });
      res.on("end", () => {
        try {
          const data = JSON.parse(body);
          resolve(typeof data.api === "number" ? data.api : 0);
        } catch {
          resolve(0);
        }
      });
    });
    req.on("error", () => resolve(0));
    req.setTimeout(timeoutMs, () => {
      req.destroy();
      resolve(0);
    });
  });
}

function backendHealthy(timeoutMs = 1200): Promise<boolean> {
  return backendApiVersion(timeoutMs).then(v => v >= BACKEND_API_VERSION);
}

async function waitForBackend(timeoutMs = 15000) {
  const started = Date.now();
  while (Date.now() - started < timeoutMs) {
    if (await backendHealthy(900)) return true;
    await new Promise(resolve => setTimeout(resolve, 500));
  }
  return false;
}

function usesExternalDevBackend() {
  return Boolean(process.env.VITE_DEV_SERVER_URL) && !app.isPackaged;
}

function nudgeBackendReload() {
  const routesPath = path.join(backendRoot(), "app", "api", "routes.py");
  if (!fs.existsSync(routesPath)) return;
  const now = new Date();
  fs.utimesSync(routesPath, now, now);
}

async function startBackend() {
  if (await backendHealthy(3000)) return;

  if (usesExternalDevBackend()) {
    const ok = await waitForBackend(45000);
    if (!ok) {
      console.error("[backend] npm:backend chưa sẵn sàng (cần api>=2). Hãy dừng và chạy lại npm run dev.");
    }
    return;
  }

  await killBackendPort();
  if (backend) {
    backend.kill();
    backend = null;
  }
  await new Promise(resolve => setTimeout(resolve, 1200));

  if (backend) return;

  const bRoot = backendRoot();

  // Ưu tiên 1: onedir bundle mới — backend/dist/_pyi_out/run_backend/
  const onedirExeFresh = path.join(bRoot, "dist", "_pyi_out", "run_backend", "run_backend.exe");
  // Ưu tiên 2: onedir bundle legacy — backend/dist/run_backend/
  const onedirExe = path.join(bRoot, "dist", "run_backend", "run_backend.exe");
  // Ưu tiên 3: onefile bundle — backend/run_backend.exe
  const onefileExe = path.join(bRoot, "run_backend.exe");

  const bundledExe = [onedirExeFresh, onedirExe, onefileExe].find(p => fs.existsSync(p));
  if (bundledExe) {
    backend = spawn(bundledExe, [], {
      cwd: path.dirname(bundledExe),
      windowsHide: true,
      env: { ...process.env, PYTHONUTF8: "1" }
    });
  } else {
    // Dev mode: chạy uvicorn trực tiếp với Python venv
    const localPython = path.join(bRoot, ".venv", "Scripts", "python.exe");
    const pythonBin = fs.existsSync(localPython) ? localPython : "python";
    backend = spawn(pythonBin, ["-m", "uvicorn", "app.main:app", "--host", "127.0.0.1", "--port", String(BACKEND_PORT), "--reload", "--reload-exclude", "data"], {
      cwd: bRoot,
      windowsHide: true,
      env: { ...process.env, PYTHONUTF8: "1" }
    });
  }

  backend.stdout.on("data", data => console.log(`[backend] ${data}`));
  backend.stderr.on("data", data => console.error(`[backend] ${data}`));
  backend.on("exit", code => {
    console.log(`Backend exited: ${code}`);
    backend = null;
  });

  await waitForBackend();
}


// ============================================================
// WINDOWS
// ============================================================
let mainWin: BrowserWindow | null = null;
let activationWin: BrowserWindow | null = null;

function createMainWindow() {
  Menu.setApplicationMenu(null);
  mainWin = new BrowserWindow({
    width: 1320,
    height: 860,
    minWidth: 1024,
    minHeight: 700,
    autoHideMenuBar: true,
    title: "Story Clone",
    icon: path.join(app.getAppPath(), "icon.png"),
    backgroundColor: "#f5f2ec",
    webPreferences: {
      preload: path.join(__dirname, "preload.js"),
      contextIsolation: true,
      nodeIntegration: false
    }
  });
  const devUrl = process.env.VITE_DEV_SERVER_URL || "http://127.0.0.1:5173";
  if (app.isPackaged) {
    mainWin.loadFile(path.join(app.getAppPath(), "dist", "index.html"));
  } else {
    mainWin.loadURL(devUrl);
  }
  mainWin.maximize();
}

function createActivationWindow() {
  Menu.setApplicationMenu(null);
  activationWin = new BrowserWindow({
    width: 560,
    height: 680,
    resizable: false,
    maximizable: false,
    autoHideMenuBar: true,
    title: "Kích hoạt Story Clone",
    icon: path.join(app.getAppPath(), "icon.png"),
    backgroundColor: "#0f0c24",
    webPreferences: {
      // Activation page dùng ipcRenderer trực tiếp (contextIsolation: false)
      // vì đây là trang nội bộ tin cậy, không load web ngoài
      nodeIntegration: true,
      contextIsolation: false
    }
  });

  activationWin.loadFile(path.join(__dirname, "activation.html"));

  // Khi người dùng đóng cửa sổ activation → thoát app
  activationWin.on("closed", () => {
    activationWin = null;
    if (!mainWin) app.quit();
  });
}

// ============================================================
// APP READY
// ============================================================
app.whenReady().then(async () => {
  await startBackend();

  // --- IPC: License ---
  ipcMain.handle("get-machine-code", () => getMachineCode());

  ipcMain.handle("verify-license", (_event, key: string) => {
    const ok = verifyKey(key);
    if (ok) saveLicense(key);
    return { ok };
  });

  ipcMain.handle("check-license", () => ({ ok: loadLicense() }));

  // Khi activation thành công → đóng activation, mở main app
  ipcMain.on("license-accepted", () => {
    if (activationWin) {
      activationWin.destroy();
      activationWin = null;
    }
    createMainWindow();
  });

  // --- IPC: File dialogs ---
  ipcMain.handle("pick-file", async () => {
    const res = await dialog.showOpenDialog({ properties: ["openFile"], filters: [{ name: "Text", extensions: ["txt", "md", "markdown", "json"] }] });
    return res.canceled ? null : res.filePaths[0];
  });
  ipcMain.handle("pick-folder", async () => {
    const res = await dialog.showOpenDialog({ properties: ["openDirectory"] });
    return res.canceled ? null : res.filePaths[0];
  });
  ipcMain.handle("save-file", async (_event, defaultName: string, ext: string) => {
    const res = await dialog.showSaveDialog({
      defaultPath: defaultName,
      filters: [{ name: ext.toUpperCase(), extensions: [ext] }]
    });
    return res.canceled ? null : res.filePath;
  });
  ipcMain.handle("notify", (_event, title: string, body: string) => {
    new Notification({ title, body }).show();
  });
  ipcMain.handle("api-request", async (_event, payload: { path: string; init?: ApiRequestInit }) => {
    const path = payload?.path || "/health";
    const init = payload?.init ?? {};
    const timeoutMs = init.timeoutMs ?? apiTimeoutMs(path, init.method);
    try {
      const res = await nodeApiRequest(path, { ...init, timeoutMs });
      return { ok: res.status >= 200 && res.status < 300, status: res.status, body: res.body, contentType: res.contentType };
    } catch (err) {
      return { ok: false, status: 0, body: "", contentType: "", error: String(err) };
    }
  });
  ipcMain.handle("restart-backend", async () => {
    if (usesExternalDevBackend()) {
      nudgeBackendReload();
      await new Promise(resolve => setTimeout(resolve, 2500));
      const version = await backendApiVersion(5000);
      return { ok: version >= BACKEND_API_VERSION, api: version };
    }
    await killBackendPort();
    if (backend) {
      backend.kill();
      backend = null;
    }
    await new Promise(resolve => setTimeout(resolve, 1200));
    await startBackend();
    const version = await backendApiVersion(3000);
    return { ok: version >= BACKEND_API_VERSION, api: version };
  });

  // --- Kiểm tra license và mở cửa sổ phù hợp ---
  if (loadLicense()) {
    createMainWindow();
  } else {
    createActivationWindow();
  }
});

app.on("window-all-closed", () => {
  if (process.platform !== "darwin") app.quit();
});

app.on("before-quit", () => {
  if (backend) backend.kill();
});
