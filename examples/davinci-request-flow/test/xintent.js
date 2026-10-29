// Compares core.validateIntent with the runner's gitops.DecodeIntent on the same intents.
const core = require('./load_core');
const fs = require('fs');
const fixtures = JSON.parse(fs.readFileSync(process.argv[2], 'utf8'));
const goOut = JSON.parse(fs.readFileSync(process.argv[3], 'utf8'));
let failures = 0;
fixtures.forEach((f, i) => {
  let js = { ok: true, message: '' };
  try { core.validateIntent(JSON.parse(f.json)); } catch (e) { js = { ok: false, message: e.message }; }
  const g = goOut[i];
  if (g.ok !== js.ok || g.message !== js.message) {
    failures++;
    console.log('MISMATCH ' + f.name + '\n  go: ' + JSON.stringify(g) + '\n  js: ' + JSON.stringify(js));
  }
});
console.log(fixtures.length + ' intent fixtures, ' + failures + ' mismatches');
process.exit(failures ? 1 : 0);
