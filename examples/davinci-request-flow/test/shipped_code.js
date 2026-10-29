// Every custom function in the exports must be byte-identical to what the
// Node tests load (same wrapper, same library transform, same step body).
const fs = require('fs');
const { source } = require('./load_fn');
let n = 0;
for (const file of process.argv.slice(2)) {
  const flow = JSON.parse(fs.readFileSync(file, 'utf8'));
  for (const node of flow.graphData.elements.nodes) {
    if (node.data.capabilityName !== 'customFunction') { continue; }
    const code = node.data.properties.code.value;
    const step = /\/\/ ---- step: ([a-z_]+) ----/.exec(code)[1];
    if (code !== source(step)) { console.error('MISMATCH ' + file + ' ' + node.data.id + ' ' + step); process.exit(1); }
    n++;
  }
}
console.log('shipped code identical to tested code in ' + n + ' function nodes');
