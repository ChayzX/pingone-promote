// Loads lib/core.js the way the generator inlines it (no module system).
const fs = require('fs');
const path = require('path');
const src = fs.readFileSync(path.join(__dirname, '..', 'lib', 'core.js'), 'utf8');
// eslint-disable-next-line no-new-func
module.exports = new Function('"use strict";\n' + src + '\nreturn core;')();
