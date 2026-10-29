// Runs the JS port on the same fixtures as the Go harness and diffs results.
const core = require('./load_core');
const fs = require('fs');

const fixtures = JSON.parse(fs.readFileSync(process.argv[2], 'utf8'));
const goOut = JSON.parse(fs.readFileSync(process.argv[3], 'utf8'));
const canon = (v) => core.canonical(v === undefined ? null : v);

let failures = 0;
let compared = 0;
fixtures.forEach((f, i) => {
  const g = goOut[i];
  // Plan review and drift now run only in the GitLab runner; the flow needs
  // BuildRequest's rules and Redact.
  const js = { name: f.name, buildError: '', build: null, redacted: core.redact(f.redact) };
  try { js.build = core.buildRequest(f.source, f.target, f.selected, f.mappings, 'd'); } catch (e) { js.buildError = e.message; }
  for (const k of ['buildError', 'build', 'redacted']) {
    compared++;
    const gv = g[k];
    if (canon(gv) !== canon(js[k])) {
      failures++;
      console.log('MISMATCH ' + f.name + ' :: ' + k + '\n  go: ' + canon(gv) + '\n  js: ' + canon(js[k]));
    }
  }
});
console.log(fixtures.length + ' fixtures, ' + compared + ' comparisons, ' + failures + ' mismatches');
process.exit(failures ? 1 : 0);
