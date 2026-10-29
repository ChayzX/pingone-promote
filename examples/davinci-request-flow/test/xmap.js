// Compares core.suggestMappings with the Go app's suggestMappings on the same fixtures.
const core = require('./load_core');
const fs = require('fs');
const fixtures = JSON.parse(fs.readFileSync(process.argv[2], 'utf8'));
const goOut = JSON.parse(fs.readFileSync(process.argv[3], 'utf8'));
let failures = 0;
fixtures.forEach((f, i) => {
  const js = core.suggestMappings({ resources: f.source }, { resources: f.target }, f.native);
  if (core.canonical(js) !== core.canonical(goOut[i] || {})) {
    failures++;
    console.log('MISMATCH ' + f.name + '\n  go: ' + JSON.stringify(goOut[i]) + '\n  js: ' + JSON.stringify(js));
  }
});
console.log(fixtures.length + ' mapping-suggestion fixtures, ' + failures + ' mismatches');
process.exit(failures ? 1 : 0);
