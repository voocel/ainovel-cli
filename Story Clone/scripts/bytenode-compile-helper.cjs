'use strict';

/**
 * Compile Electron main/preload JS to .jsc using bytenode.
 * Must use electronMain: true on Electron >= 42 (V8 >= 14.8).
 *
 * Usage: node scripts/bytenode-compile-helper.cjs <input.js> <output.jsc>
 */

const fs = require('node:fs');
const path = require('node:path');
const bytenode = require('bytenode');

async function main() {
  const input = process.argv[2];
  const output = process.argv[3];
  if (!input || !output) {
    console.error('Usage: node scripts/bytenode-compile-helper.cjs <input.js> <output.jsc>');
    process.exit(1);
  }

  const root = path.resolve(__dirname, '..');
  const electronPath = process.platform === 'win32'
    ? path.join(root, 'node_modules', 'electron', 'dist', 'electron.exe')
    : path.join(root, 'node_modules', 'electron', 'dist', 'electron');

  // ELECTRON_RUN_AS_NODE makes require('electron') return a path string, breaking electronMain compile.
  delete process.env.ELECTRON_RUN_AS_NODE;

  // #region agent log
  try{require('node:fs').appendFileSync(require('node:path').join(__dirname,'..','debug-417abf.log'),JSON.stringify({sessionId:'417abf',hypothesisId:'A',location:'bytenode-compile-helper.cjs:pre',message:'compile start',data:{input:path.basename(input),electronRunAsNode:process.env.ELECTRON_RUN_AS_NODE??null,electronExists:fs.existsSync(electronPath)},timestamp:Date.now()})+'\n');}catch(_){}
  // #endregion

  if (!fs.existsSync(input)) {
    console.error(`Input not found: ${input}`);
    process.exit(1);
  }
  if (!fs.existsSync(electronPath)) {
    console.error(`Electron not found: ${electronPath}`);
    process.exit(1);
  }

  console.log(`  electronMain compile: ${path.basename(input)}`);
  console.log(`  electron: ${electronPath}`);

  await bytenode.compileFile({
    filename: path.resolve(input),
    output: path.resolve(output),
    compileAsModule: true,
    electronMain: true,
    electronPath,
  });

  if (!fs.existsSync(output)) {
    console.error(`Output not created: ${output}`);
    process.exit(1);
  }
  console.log(`  OK: ${path.basename(output)} (${fs.statSync(output).size} bytes)`);

  // #region agent log
  try{require('node:fs').appendFileSync(require('node:path').join(__dirname,'..','debug-417abf.log'),JSON.stringify({sessionId:'417abf',hypothesisId:'A',location:'bytenode-compile-helper.cjs:ok',message:'compile success',data:{output:path.basename(output),bytes:fs.statSync(output).size},timestamp:Date.now()})+'\n');}catch(_){}
  // #endregion
}

main().catch((err) => {
  console.error('bytenode compile failed:', err);
  process.exit(1);
});
