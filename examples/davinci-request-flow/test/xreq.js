// Builds each request fixture with the JS port and writes results for the Go
// cross-check (cmd/xreq), which compares bytes and runs gitops.Decode on them.
const core = require('./load_core');
const fs = require('fs');

const fixtures = JSON.parse(fs.readFileSync(process.argv[2], 'utf8'));
const out = fixtures.map((f) => {
  try {
    const r = core.newRequest(f.source, f.target, f.sourceRegion, f.targetRegion, f.selected, f.mappings, f.description, f.id, f.createdAt);
    return { name: f.name, error: '', json: r.json };
  } catch (e) {
    return { name: f.name, error: e.message, json: '' };
  }
});
fs.writeFileSync(process.argv[3], JSON.stringify(out));
console.log('js built ' + out.filter((o) => !o.error).length + ' of ' + out.length + ' requests');
