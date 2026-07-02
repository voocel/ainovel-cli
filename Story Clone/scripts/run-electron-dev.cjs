const { spawnSync } = require("node:child_process");
const path = require("node:path");
const fs = require("node:fs");

const env = { ...process.env };
delete env.ELECTRON_RUN_AS_NODE;

const win = process.platform === "win32";
const tsc = '"' + path.resolve(__dirname, "..", "node_modules", ".bin", win ? "tsc.cmd" : "tsc") + '"';
const electron = '"' + path.resolve(__dirname, "..", "node_modules", ".bin", win ? "electron.cmd" : "electron") + '"';
const spawnOpts = { stdio: "inherit", env, shell: win };

let result = spawnSync(tsc, ["-p", "electron/tsconfig.json"], spawnOpts);
if (result.status !== 0) process.exit(result.status ?? 1);

// Copy activation.html → dist-electron/
const srcHtml = path.resolve(__dirname, "..", "electron", "activation.html");
const dstHtml = path.resolve(__dirname, "..", "dist-electron", "activation.html");
if (fs.existsSync(srcHtml)) {
  fs.copyFileSync(srcHtml, dstHtml);
  console.log("[dev] Copied activation.html → dist-electron/");
}

result = spawnSync(electron, ["."], {
  ...spawnOpts,
  env: { ...env, NODE_ENV: "development", VITE_DEV_SERVER_URL: "http://127.0.0.1:5173" },
  cwd: path.resolve(__dirname, "..")
});
process.exit(result.status ?? 0);
