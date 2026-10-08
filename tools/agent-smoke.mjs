// Runs every example in a running server's /AGENTS.md the way an agent would, and reports
// status, latency and size. Exits non-zero if any example fails.
//
//   node tools/agent-smoke.mjs [http://127.0.0.1:8088]
const base = (process.argv[2] ?? 'http://127.0.0.1:8088').replace(/\/$/, '');

const guide = await (await fetch(`${base}/AGENTS.md`)).text();
const examples = [...guide.matchAll(/curl -s '([^']+)'/g)].map((m) => m[1]);
if (examples.length < 10) {
  console.error(`only found ${examples.length} examples in ${base}/AGENTS.md`);
  process.exit(1);
}

let failed = 0;
const rows = [];
for (const url of examples) {
  const t0 = performance.now();
  const res = await fetch(url, { headers: { 'accept-encoding': 'gzip' } });
  const body = await res.text();
  const ms = Math.round(performance.now() - t0);
  let ok = res.status === 200;
  let note = '';
  try {
    const j = JSON.parse(body);
    if (j.error) { ok = false; note = j.error.message; }
    else if (Array.isArray(j.results)) note = `${j.results.length} results${j.total != null ? ` of ${j.total}` : ''}`;
  } catch { ok = false; note = 'not JSON'; }
  if (!ok) failed++;
  rows.push({ ok, status: res.status, ms, kb: (body.length / 1024).toFixed(1), url: url.replace(base, ''), note });
}
for (const r of rows) {
  console.log(`${r.ok ? 'ok  ' : 'FAIL'} ${r.status} ${String(r.ms).padStart(5)}ms ${r.kb.padStart(7)}KB  ${r.url.slice(0, 92)}${r.note ? '  → ' + r.note : ''}`);
}
console.log(`\n${rows.length - failed}/${rows.length} examples worked; slowest ${Math.max(...rows.map((r) => r.ms))}ms`);
process.exit(failed ? 1 : 0);
