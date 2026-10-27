#!/usr/bin/env node
// Turns the dashboard's coverage from browser runs (web/scripts/coverage/collect.mjs) into lcov
// against web/src (D195), through the source maps of the coverage build (METARUSH_WEB_SOURCEMAP=1:
// hidden maps beside unminified bundles in web/dist/assets). scripts/coverage.sh runs it.
//
//   node web/scripts/coverage/report.mjs --out .coverage/web-e2e.lcov <folder with collect.mjs records>…
//
// Every record of a bundle is merged first, function by function (a function is one by its range in
// the bundle; each of its ranges keeps the highest count any record gave it). A page that navigated
// away while the bundle was still loading reports only the functions it had compiled, under a whole-
// bundle range that ran: converted on its own it would mark everything else as run.
import { existsSync, readFileSync, readdirSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import libCoverage from 'istanbul-lib-coverage';
import libReport from 'istanbul-lib-report';
import reports from 'istanbul-reports';
import v8toIstanbul from 'v8-to-istanbul';

const here = path.dirname(fileURLToPath(import.meta.url));
const web = path.resolve(here, '..', '..');
const assets = path.join(web, 'dist', 'assets');
const args = process.argv.slice(2);
const outAt = args.indexOf('--out');
const out = outAt >= 0 ? path.resolve(args[outAt + 1]) : path.resolve('web-e2e.lcov');
const dirs = args.filter((a, i) => a !== '--out' && i !== outAt + 1);
if (!dirs.length) {
  console.error('usage: report.mjs --out <lcov file> <record folder>…');
  process.exit(2);
}

const sources = new Map(); // bundle file name -> its text as the browser ran it
const merged = new Map(); // bundle file name -> Map(function key -> {functionName, isBlockCoverage, ranges: Map})
let records = 0;
for (const dir of dirs) {
  for (const name of readdirSync(dir)) {
    const p = path.join(dir, name);
    if (name.startsWith('source-')) {
      const { file, source } = JSON.parse(readFileSync(p, 'utf8'));
      sources.set(file, source);
      continue;
    }
    if (!name.startsWith('page-')) continue;
    for (const { file, functions } of JSON.parse(readFileSync(p, 'utf8'))) {
      records++;
      let fns = merged.get(file);
      if (!fns) merged.set(file, (fns = new Map()));
      for (const fn of functions) {
        const root = fn.ranges[0];
        const key = `${root.startOffset}:${root.endOffset}`;
        let cur = fns.get(key);
        if (!cur) fns.set(key, (cur = { functionName: fn.functionName, isBlockCoverage: false, ranges: new Map() }));
        cur.isBlockCoverage ||= fn.isBlockCoverage;
        for (const r of fn.ranges) {
          const rk = `${r.startOffset}:${r.endOffset}`;
          const prev = cur.ranges.get(rk);
          if (!prev || r.count > prev.count) cur.ranges.set(rk, { startOffset: r.startOffset, endOffset: r.endOffset, count: r.count });
        }
      }
    }
  }
}

// Outer before inner: by start, then the longer first.
const byRange = (a, b) => a.startOffset - b.startOffset || b.endOffset - a.endOffset;
const map = libCoverage.createCoverageMap({});
let skipped = 0;
for (const [file, fns] of merged) {
  const source = sources.get(file);
  const mapFile = path.join(assets, `${file}.map`);
  if (!source || !existsSync(mapFile)) {
    skipped++;
    continue;
  }
  const functions = [...fns.values()]
    .map((f) => {
      const ranges = [...f.ranges.values()];
      const root = ranges.reduce((a, b) => (b.endOffset - b.startOffset > a.endOffset - a.startOffset ? b : a));
      return { functionName: f.functionName, isBlockCoverage: f.isBlockCoverage, ranges: [root, ...ranges.filter((r) => r !== root).sort(byRange)] };
    })
    .sort((a, b) => byRange(a.ranges[0], b.ranges[0]));
  // The bundle's own path, so the map's relative sources land in web/src.
  const converter = v8toIstanbul(path.join(assets, file), 0, { source, sourceMap: { sourcemap: JSON.parse(readFileSync(mapFile, 'utf8')) } });
  await converter.load();
  converter.applyCoverage(functions);
  for (const [f, cov] of Object.entries(converter.toIstanbul())) {
    if (f.startsWith(path.join(web, 'src') + path.sep)) map.merge({ [f]: cov });
  }
  converter.destroy();
}

const context = libReport.createContext({ dir: path.dirname(out), coverageMap: map, defaultSummarizer: 'flat' });
reports.create('lcovonly', { file: path.basename(out), projectRoot: web }).execute(context);
const s = map.getCoverageSummary();
const line = `${s.lines.covered} of ${s.lines.total} lines (${s.lines.pct}%) in ${map.files().length} files of web/src, from ${records} page record(s)`;
writeFileSync(`${out}.summary.txt`, `${line}\n`);
console.log(`dashboard in browser runs: ${line}` + (skipped ? `; ${skipped} bundle(s) without a source map skipped (build the dashboard with METARUSH_WEB_SOURCEMAP=1)` : ''));
