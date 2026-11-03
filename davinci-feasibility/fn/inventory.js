// Stage 2: one page of flows, connector instances or variables. Follows
// _links.next on the same origin and path, then moves to the next collection.
// When all three are read: logical keys, ID-to-key maps and the flows to fetch.
const s = lib.state(params.state);
const frags = lib.obj(params.frags);
if (!frags.inv) { frags.inv = { connections: [], variables: [] }; }
if (!frags.flows) { frags.flows = {}; }
const e = lib.enter(s);
const body = lib.parse(params.body);
const kind = lib.KINDS[s.inv.k];
lib.call(s, 'inventory', e.since, params.statusCode, lib.size(body), 0);
const done = (action, progress) => {
  lib.leave(s, 'inventory', e.t0);
  lib.track(s, frags);
  return lib.out(s, action, progress, { frags: frags });
};
const items = body && body._embedded && body._embedded[kind.key];
if (!lib.ok2xx(params.statusCode) || !Array.isArray(items)) {
  lib.fail(s, 'inventory', lib.errInfo('list ' + kind.name, params.statusCode, body));
  if (kind.name === 'flows') {
    lib.end(s, 'inventory', 'error', e.t0);
    return done('finish', 'Inventory failed');
  }
  // Connections or variables unreadable: keep going, the translation is then incomplete.
  lib.note(s, 'Could not list ' + kind.name + '; the snippet lacks them.');
} else {
  s.inv.pages += 1;
  for (const it of items) {
    if (!it || typeof it !== 'object' || typeof it.id !== 'string') { continue; }
    if (kind.name === 'flows') {
      s.inv.flowList.push({ id: it.id, name: String(it.name || '') });
    } else if (kind.name === 'connectorInstances') {
      frags.inv.connections.push({ id: it.id, name: String(it.name || ''), connector: { id: String((it.connector && it.connector.id) || '') }, properties: it.properties || {} });
    } else {
      frags.inv.variables.push({ id: it.id, name: String(it.name || ''), context: String(it.context || ''), dataType: it.dataType, mutable: it.mutable,
        displayName: it.displayName, min: it.min, max: it.max, value: it.value, flow: it.flow && it.flow.id ? { id: String(it.flow.id) } : undefined });
    }
  }
  lib.stage(s, 'inventory').items += items.length;
  const next = lib.nextLink(body);
  if (next) {
    const a = lib.splitUrl(next);
    const f = lib.splitUrl(s.inv.first);
    if (!a || !f || a.origin !== f.origin || a.path !== f.path || s.inv.seen.indexOf(next) >= 0 || s.inv.pages >= 200) {
      lib.fail(s, 'inventory', { where: 'list ' + kind.name, status: 0, code: 'BAD_PAGING', message: 'next link left the collection, repeated, or exceeded 200 pages' });
      lib.end(s, 'inventory', 'error', e.t0);
      return done('finish', 'Inventory failed');
    }
    s.inv.seen.push(next);
    s.url = next;
    return done(lib.shouldPause(s) ? 'pause' : 'more', 'Listing ' + kind.name + ' (page ' + (s.inv.pages + 1) + ')');
  }
}
// Next collection.
s.inv.k += 1;
if (s.inv.k < lib.KINDS.length) {
  s.url = lib.inventoryUrl(s, s.inv.k);
  s.inv.first = s.url;
  s.inv.seen = [];
  s.inv.pages = 0;
  return done(lib.shouldPause(s) ? 'pause' : 'more', 'Listing ' + lib.KINDS[s.inv.k].name);
}
// All read: logical keys from names. Same-name resources are a collision the
// user must resolve; the later one (by ID) gets a suffix so the run can go on.
const keyed = (list) => {
  const map = {};
  const used = {};
  let collisions = 0;
  for (const it of list.slice().sort((a, b) => (a.id < b.id ? -1 : a.id > b.id ? 1 : 0))) {
    let k;
    try { k = tr.key(it.name); } catch (err) { k = 'unnamed'; }
    if (used[k]) { collisions += 1; used[k] += 1; k = k + '--' + used[k]; } else { used[k] = 1; }
    map[it.id] = k;
  }
  return { map: map, collisions: collisions };
};
const fk = keyed(s.inv.flowList);
const ck = keyed(frags.inv.connections);
const vars = {};
let varProblems = 0;
for (const v of frags.inv.variables) {
  try {
    vars[v.id] = tr.variableKey(v.name, v.context, v.context === 'flow' ? fk.map[(v.flow && v.flow.id) || ''] || '' : '');
  } catch (err) { varProblems += 1; }
}
s.maps = { flows: fk.map, conns: ck.map, vars: vars, env: s.cfg.sourceEnvironmentId };
s.counts = { flowsInEnv: s.inv.flowList.length, connectorInstances: frags.inv.connections.length, variables: frags.inv.variables.length,
  flowNameCollisions: fk.collisions, connectionNameCollisions: ck.collisions, variablesWithoutKey: varProblems };
if (fk.collisions || ck.collisions) { lib.note(s, 'Some flows or connections share a name; their keys got a --N suffix. The hosted design refuses this.'); }
const ordered = s.inv.flowList.map((x) => x.id).sort((a, b) => (fk.map[a] < fk.map[b] ? -1 : fk.map[a] > fk.map[b] ? 1 : 0));
const n = s.requested > 0 ? Math.min(s.requested, ordered.length) : ordered.length;
s.fetch = { ids: ordered.slice(0, n), i: 0, ok: 0, failed: 0 };
s.counts.flowsRequested = s.requested > 0 ? s.requested : ordered.length;
delete s.inv.flowList;
lib.end(s, 'inventory', s.stages.inventory.errors ? 'warn' : 'ok', e.t0);
lib.begin(s, 'fetch', e.t0);
if (!n) {
  lib.end(s, 'fetch', 'ok', e.t0);
  return done('assemble', 'No flows to fetch');
}
s.phase = 'fetch';
s.url = lib.flowUrl(s, s.fetch.ids[0]);
return done(lib.shouldPause(s) ? 'pause' : 'fetch', 'Fetching flow 1 of ' + n);
