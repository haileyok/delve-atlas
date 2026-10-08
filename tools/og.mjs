// Renders the link-preview card (og.jpg) from the running server's newest map.
//
//   node tools/og.mjs [--base http://127.0.0.1:8088] [--out data/atlas/latest/og.jpg]
//
// The server draws nothing itself: it serves /og/card.html, this opens it in headless Chromium and
// saves the 1200x630 screenshot. Run it after each map rebuild (scripts/rebuild-loop.sh does).
//
// The card is a JPEG because thousands of anti-aliased dots make a lossless PNG over 500 KB, and
// some chat apps drop previews much above 300 KB. With ImageMagick installed the screenshot is
// re-encoded at quality 90 with full-resolution colour (about 250 KB, no smeared dots); without it
// the browser's own JPEG encoder is used, which subsamples colour (about 230 KB, slightly softer).
import { execFileSync } from 'node:child_process';
import { renameSync, rmSync, statSync } from 'node:fs';
import { withPage } from './cdp.mjs';

const arg = (name, dflt) => {
  const i = process.argv.indexOf(`--${name}`);
  return i > 0 ? process.argv[i + 1] : dflt;
};
const base = arg('base', 'http://127.0.0.1:8088');
const out = arg('out', 'og.jpg');
const png = `${out}.tmp.png`;
const jpg = `${out}.tmp.jpg`;

const info = await withPage(async (page) => {
  await page.goto(`${base}/og/card.html`);
  await page.waitFor('window.__ogReady === true', 60000);
  const info = await page.eval('window.__ogInfo');
  await page.screenshot(png);
  // the fallback, taken while the page is still open
  await page.screenshot(`${jpg}.browser`, { format: 'jpeg', quality: 95 });
  return { info, logs: page.logs };
}, { width: 1200, height: 630, dpr: 1 });

let encoder = 'browser';
for (const bin of ['magick', 'convert']) {
  try {
    execFileSync(bin, [png, '-strip', '-quality', '90', '-sampling-factor', '4:4:4', jpg], { stdio: 'ignore' });
    encoder = bin;
    break;
  } catch {}
}
if (encoder === 'browser') renameSync(`${jpg}.browser`, jpg);
else rmSync(`${jpg}.browser`, { force: true });
rmSync(png, { force: true });

if (info.logs.length) console.error(info.logs.join('\n'));
renameSync(jpg, out);
console.log(`wrote ${out} (${(statSync(out).size / 1024).toFixed(0)} KB, ${encoder} encoder) for snapshot ${info.info.snapshot}; regions named: ${info.info.labels.join(', ')}`);
