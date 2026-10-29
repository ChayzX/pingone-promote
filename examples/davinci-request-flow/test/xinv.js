// Compares core.inventoryFromSnapshots with the Go client on the same responses.
const core = require('./load_core');
const fs = require('fs');

const ORIGIN = 'https://api.pingone.com';
const fixtures = JSON.parse(fs.readFileSync(process.argv[2], 'utf8'));
const goOut = JSON.parse(fs.readFileSync(process.argv[3], 'utf8'));
const sub = (v) => JSON.parse(JSON.stringify(v).split('ORIGIN').join(ORIGIN));

let failures = 0;
fixtures.forEach((f, i) => {
  const g = goOut[i];
  let js;
  try {
    const pages = sub(f.pages);
    if (!core.pagesComplete(pages)) { throw new Error('incomplete PingOne pagination'); }
    const inv = core.inventoryFromSnapshots(ORIGIN, f.env, f.envRecord, f.snapshot, pages, !!f.pin, f.pin || f.snapshot.snapshotId);
    js = { error: false, org: inv.organizationId, snapshot: inv.snapshotId,
      resources: inv.resources.map((r) => ({ id: r.id, url: r.url.slice(ORIGIN.length), name: r.name, type: r.type, configuration: r.configuration, dependencies: r.dependencies })) };
  } catch (e) {
    js = { error: true, message: e.message };
  }
  const gv = g.error ? { error: true } : { error: false, org: g.org, snapshot: g.snapshot, resources: (g.resources || []).map((r) => ({ id: r.id, url: r.url, name: r.name, type: r.type, configuration: r.configuration || {}, dependencies: r.dependencies || [] })) };
  const jv = js.error ? { error: true } : js;
  if (core.canonical(gv) !== core.canonical(jv)) {
    failures++;
    console.log('MISMATCH ' + f.name + '\n  go: ' + JSON.stringify(g) + '\n  js: ' + JSON.stringify(js));
  }
});
console.log(fixtures.length + ' inventory fixtures, ' + failures + ' mismatches');
process.exit(failures ? 1 : 0);
