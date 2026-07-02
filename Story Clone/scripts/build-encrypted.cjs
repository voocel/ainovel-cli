/**
 * build-encrypted.cjs
 * ====================
 * Script build đầy đủ cho Story Clone:
 *   1. Build PyInstaller backend (onedir) → backend/dist/run_backend/
 *   2. Compile TypeScript (electron) + Vite (renderer)
 *   3. Copy activation.html → dist-electron/
 *   4. Mã hoá main.js + preload.js → .jsc (V8 bytecode) bằng bytenode
 *   5. Tạo launcher stubs + copy bytenode runtime
 *   6. Chạy electron-builder → win-unpacked + installer .exe
 *
 * Cách dùng:
 *   node scripts/build-encrypted.cjs            → full build (win-unpacked + installer)
 *   node scripts/build-encrypted.cjs --dir      → chỉ win-unpacked, không đóng gói installer
 *   node scripts/build-encrypted.cjs --skip-py  → bỏ qua bước build Python (dùng bundle cũ)
 */

"use strict";

const { spawnSync } = require("node:child_process");
const fs   = require("node:fs");
const path = require("node:path");

const ROOT       = path.resolve(__dirname, "..");
const BACKEND    = path.join(ROOT, "backend");
const DIST_ELEC  = path.join(ROOT, "dist-electron");
const BIN        = path.join(ROOT, "node_modules", ".bin");
const WIN        = process.platform === "win32";
const DIR_ONLY   = process.argv.includes("--dir");
const SKIP_PY    = process.argv.includes("--skip-py");

// Helpers lấy đường dẫn binary từ node_modules/.bin
function bin(name) {
  const ext = WIN ? ".cmd" : "";
  return `"${path.join(BIN, name + ext)}"`;
}

// Targets để mã hoá
const TARGETS = ["main.js", "preload.js"];

// ──────────────────────────────────────────────
// Helper: chạy lệnh shell, thoát nếu thất bại
// ──────────────────────────────────────────────
function run(cmd, opts = {}) {
  console.log(`\n▶ ${cmd}`);
  const result = spawnSync(cmd, [], {
    stdio: "inherit",
    shell: true,
    cwd: ROOT,
    ...opts,
  });
  if (result.status !== 0) {
    console.error(`\n✗ Lệnh thất bại (exit ${result.status ?? "?"}): ${cmd}`);
    process.exit(result.status ?? 1);
  }
}

// ──────────────────────────────────────────────
// Bước 1: Build PyInstaller backend
// ──────────────────────────────────────────────
console.log("\n╔═══════════════════════════════════════════════════╗");
console.log("║  Story Clone — Build Script (Encrypted Edition)   ║");
console.log("╚═══════════════════════════════════════════════════╝");

if (SKIP_PY) {
  console.log("\n⏭  --skip-py: Bỏ qua bước build Python backend.");
} else {
  console.log("\n═══════════════════════════════════════════════════");
  console.log("  BƯỚC 1 — Build Python backend (PyInstaller)");
  console.log("═══════════════════════════════════════════════════");

  // Tìm PyInstaller trong venv
  const pyinstaller = path.join(BACKEND, ".venv", "Scripts", "pyinstaller.exe");
  const python      = path.join(BACKEND, ".venv", "Scripts", "python.exe");

  if (!fs.existsSync(python)) {
    console.error("✗ Không tìm thấy Python venv tại backend/.venv");
    console.error("  Chạy: cd backend && python -m venv .venv && .venv\\Scripts\\pip install -r requirements.txt pyinstaller");
    process.exit(1);
  }

  if (!fs.existsSync(pyinstaller)) {
    console.log("  PyInstaller chưa cài, đang cài...");
    run(`"${python}" -m pip install pyinstaller --quiet`, { cwd: BACKEND });
  }

  run(`node scripts/prebuild-clean.cjs`, { cwd: ROOT });

  const pyiDist = path.join(BACKEND, "dist", "_pyi_out");
  const pyiWork = path.join(BACKEND, "build", "_pyi_work");
  const bundleDir = path.join(pyiDist, "run_backend");
  const bundleExe = path.join(bundleDir, "run_backend.exe");
  const legacyDir = path.join(BACKEND, "dist", "run_backend");
  const legacyExe = path.join(legacyDir, "run_backend.exe");

  // distpath/workpath riêng — tránh xóa backend/dist/run_backend có thể đang bị process khóa.
  run(
    `"${pyinstaller}" --noconfirm --distpath "${pyiDist}" --workpath "${pyiWork}" run_backend.spec`,
    { cwd: BACKEND }
  );

  if (!fs.existsSync(bundleExe)) {
    console.error(`✗ PyInstaller thất bại, không tìm thấy: ${bundleExe}`);
    process.exit(1);
  }

  // Đồng bộ sang dist/run_backend (electron dev / legacy) nếu có thể.
  if (process.platform === "win32") {
    fs.mkdirSync(path.dirname(legacyDir), { recursive: true });
    const robocopy = spawnSync(
      "robocopy",
      [bundleDir, legacyDir, "/MIR", "/R:2", "/W:1", "/NFL", "/NDL", "/NJH", "/NJS"],
      { shell: true }
    );
    if (robocopy.status >= 8) {
      console.warn("⚠ Không đồng bộ được backend/dist/run_backend (có thể đang bị khóa).");
      console.warn("  Bản build dùng backend/dist/_pyi_out/run_backend — vẫn đóng gói bình thường.");
    }
  } else {
    try {
      fs.rmSync(legacyDir, { recursive: true, force: true });
      fs.cpSync(bundleDir, legacyDir, { recursive: true });
    } catch (err) {
      console.warn(`⚠ Không copy legacy bundle: ${err.message}`);
    }
  }

  const publishExe = fs.existsSync(legacyExe) ? legacyExe : bundleExe;
  console.log(`✓ Backend bundle: ${publishExe}`);
}

// ──────────────────────────────────────────────
// Bước 2: Build TypeScript + Vite
// ──────────────────────────────────────────────
console.log("\n═══════════════════════════════════════════════════");
console.log("  BƯỚC 2 — Biên dịch TypeScript + Vite renderer");
console.log("═══════════════════════════════════════════════════");
run(`${bin("tsc")} -p electron/tsconfig.json`);
run(`${bin("vite")} build`);

// ──────────────────────────────────────────────
// Bước 3: Copy activation.html
// ──────────────────────────────────────────────
console.log("\n═══════════════════════════════════════════════════");
console.log("  BƯỚC 3 — Copy activation.html");
console.log("═══════════════════════════════════════════════════");
const srcHtml = path.join(ROOT, "electron", "activation.html");
const dstHtml = path.join(DIST_ELEC, "activation.html");
if (fs.existsSync(srcHtml)) {
  fs.copyFileSync(srcHtml, dstHtml);
  console.log("✓ Copied activation.html → dist-electron/");
} else {
  console.warn("⚠ Không tìm thấy activation.html, bỏ qua.");
}

// ──────────────────────────────────────────────
// Bước 4: Mã hoá .js → .jsc bằng bytenode
// ──────────────────────────────────────────────
console.log("\n═══════════════════════════════════════════════════");
console.log("  BƯỚC 4 — Mã hoá V8 bytecode (.jsc) bằng bytenode");
console.log("═══════════════════════════════════════════════════");

// ELECTRON_RUN_AS_NODE khiến require('electron') trả path thay vì API → bytenode electronMain fail.
delete process.env.ELECTRON_RUN_AS_NODE;

const compileHelper = path.join(ROOT, "scripts", "bytenode-compile-helper.cjs");

if (!fs.existsSync(compileHelper)) {
  console.error(`✗ Thiếu ${compileHelper}`);
  process.exit(1);
}

for (const target of TARGETS) {
  const srcJs  = path.join(DIST_ELEC, target);
  const jscOut = path.join(DIST_ELEC, target.replace(".js", ".jsc"));

  if (!fs.existsSync(srcJs)) {
    console.warn(`[skip] ${target} not found`);
    continue;
  }

  console.log(`\n  Compiling: ${target} -> ${target.replace(".js", ".jsc")}`);

  // Electron >= 42: phải dùng electronMain (không dùng ELECTRON_RUN_AS_NODE)
  const compileEnv = { ...process.env };
  delete compileEnv.ELECTRON_RUN_AS_NODE;
  const result = spawnSync(process.execPath, [compileHelper, srcJs, jscOut], {
    stdio: "inherit",
    shell: false,
    cwd: ROOT,
    env: compileEnv,
  });

  if (result.status !== 0 || !fs.existsSync(jscOut)) {
    console.error(`  FAIL: could not compile ${target}`);
    if (result.error) console.error("  Error:", result.error.message);
    process.exit(1);
  }

  fs.unlinkSync(srcJs);
  console.log(`  OK: ${target} -> ${target.replace(".js", ".jsc")}`);
}


// ──────────────────────────────────────────────
// Bước 5: Tạo launcher stubs
// ──────────────────────────────────────────────
console.log("\n═══════════════════════════════════════════════════");
console.log("  BƯỚC 5 — Tạo launcher stubs");
console.log("═══════════════════════════════════════════════════");

const stubs = {
  "main.js": `"use strict";
// Auto-generated stub — do not edit
const path = require("node:path");
require(path.join(__dirname, "bytenode"));
require(path.join(__dirname, "main.jsc"));
`,
  "preload.js": `"use strict";
// Auto-generated stub — do not edit
const path = require("node:path");
require(path.join(__dirname, "bytenode"));
require(path.join(__dirname, "preload.jsc"));
`,
};

for (const [name, content] of Object.entries(stubs)) {
  fs.writeFileSync(path.join(DIST_ELEC, name), content, "utf-8");
  console.log(`✓ Tạo stub: dist-electron/${name}`);
}

// ──────────────────────────────────────────────
// Bước 5b: Copy bytenode runtime vào dist-electron
// ──────────────────────────────────────────────
console.log("\n═══════════════════════════════════════════════════");
console.log("  BƯỚC 5b — Copy bytenode runtime");
console.log("═══════════════════════════════════════════════════");

const bytenodeSrc = path.join(ROOT, "node_modules", "bytenode");
const bytenodeDst = path.join(DIST_ELEC, "bytenode");

if (!fs.existsSync(bytenodeSrc)) {
  console.error("✗ Không tìm thấy bytenode. Chạy: npm install --save-dev bytenode");
  process.exit(1);
}

if (fs.existsSync(bytenodeDst)) {
  fs.rmSync(bytenodeDst, { recursive: true, force: true });
}
fs.cpSync(bytenodeSrc, bytenodeDst, { recursive: true });
console.log("✓ Copied node_modules/bytenode → dist-electron/bytenode");

// ──────────────────────────────────────────────
// Bước 6: Cập nhật package.json main field tạm thời
// electron-builder đọc "main" để biết entry point
// ──────────────────────────────────────────────
// Không cần thay đổi — main.js stub vẫn tồn tại, electron-builder đọc đúng

// ──────────────────────────────────────────────
// Bước 7: Chạy electron-builder
// ──────────────────────────────────────────────
console.log("\n═══════════════════════════════════════════════════");
console.log("  BƯỚC 6 — Đóng gói với electron-builder");
console.log("═══════════════════════════════════════════════════");

const builderTarget = DIR_ONLY ? "--dir" : "--win";
run(`${bin("electron-builder")} ${builderTarget}`);

// ──────────────────────────────────────────────
// Xong!
// ──────────────────────────────────────────────
console.log("\n╔═══════════════════════════════════════════════════╗");
console.log("║  ✅  BUILD HOÀN THÀNH                             ║");
console.log("╚═══════════════════════════════════════════════════╝");
console.log("  📁 win-unpacked : dist-packaged/win-unpacked/");
if (!DIR_ONLY) {
  console.log("  📦 Installer    : dist-packaged/Story Clone Setup *.exe");
}
console.log("\n  Nội dung win-unpacked bao gồm:");
console.log("    ├── resources/backend/dist/_pyi_out/run_backend/  ← Python bundled (không cần cài Python)");
console.log("    ├── resources/dist-electron/main.jsc     ← Electron main (V8 bytecode)");
console.log("    ├── resources/dist-electron/preload.jsc  ← Preload (V8 bytecode)");
console.log("    └── resources/dist/                      ← React UI");
console.log();
