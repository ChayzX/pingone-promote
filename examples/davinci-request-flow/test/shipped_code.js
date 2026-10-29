// Every custom function in the exports must be byte-identical to what the
// Node tests load (same wrapper, same library transform, same step body).
const fs = require('fs');
const { source } = require('./load_fn');
let n = 0;
let scripts = 0;
for (const file of process.argv.slice(2)) {
  const data = JSON.parse(fs.readFileSync(file, 'utf8'));
  // A main flow with its subflows ({"flows": [...]}) or a single flow.
  for (const node of (data.flows || [data]).flatMap((fl) => fl.graphData.elements.nodes)) {
    // Screen scripts (the node's Script field) must at least parse.
    const script = node.data.properties && node.data.properties.customScript && node.data.properties.customScript.value;
    if (script) {
      // eslint-disable-next-line no-new-func
      try { new Function(script); scripts++; } catch (e) { console.error('SCRIPT SYNTAX ' + file + ' ' + node.data.id + ': ' + e.message); process.exit(1); }
    }
    if (node.data.capabilityName !== 'customFunction') { continue; }
    const code = node.data.properties.code.value;
    const step = /\/\/ ---- step: ([a-z_]+) ----/.exec(code)[1];
    if (code !== source(step)) { console.error('MISMATCH ' + file + ' ' + node.data.id + ' ' + step); process.exit(1); }
    n++;
  }
}
console.log('shipped code identical to tested code in ' + n + ' function nodes; ' + scripts + ' screen scripts parse');
