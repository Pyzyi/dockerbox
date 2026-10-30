// Copies the pinned terminal emulator files into the embedded static assets.
// Usage: node scripts/vendor.mjs [outDir]   (default: ../internal/web/static/vendor)
import { copyFileSync, mkdirSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const root = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const out = resolve(process.argv[2] ?? join(root, "../internal/web/static/vendor"));
const files = {
  "@xterm/xterm/lib/xterm.mjs": "xterm.js",
  "@xterm/xterm/css/xterm.css": "xterm.css",
  "@xterm/addon-fit/lib/addon-fit.mjs": "addon-fit.js",
  "@xterm/xterm/LICENSE": "LICENSE.xterm.txt",
};
mkdirSync(out, { recursive: true });
for (const [src, dst] of Object.entries(files)) {
  copyFileSync(join(root, "node_modules", src), join(out, dst));
}
console.log(`vendored ${Object.keys(files).length} files to ${out}`);
