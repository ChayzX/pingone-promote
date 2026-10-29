// Wraps a step function exactly as the generator does for DaVinci:
// module.exports = a = async ({params}) => { <core.js> <step body> };
const fs = require('fs');
const path = require('path');
const root = path.join(__dirname, '..');
// Same transform as gen_flows.py: drop full-line comments and indentation.
const core = fs.readFileSync(path.join(root, 'lib', 'core.js'), 'utf8').split('\n').map((l) => l.trim())
  .filter((l) => l && !l.startsWith('//')).join('\n');

function source(name) {
  const body = fs.readFileSync(path.join(root, 'flows', 'fn', name + '.js'), 'utf8');
  // The library runs in its own scope so step code can never collide with it.
  return 'module.exports = a = async ({params}) => {\nconst core = (() => {\n' + core + '\nreturn core;\n})();\n// ---- step: ' + name + ' ----\n' + body + '\n};\n';
}

function load(name) {
  const m = { exports: null };
  // eslint-disable-next-line no-new-func
  new Function('module', 'a', '"use strict";\n' + source(name).replace('module.exports = a = ', 'module.exports = '))(m);
  return m.exports;
}

module.exports = { source, load };
