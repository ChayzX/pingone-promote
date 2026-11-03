// Collect one page of a read-only list (id and name only), follow next links,
// then search. The list is flows, applications, or the flow policies of one
// application (s.list.kind). The search never picks anything by itself (#93).
// The response bodies can carry more than names (an application can hold
// client secrets); only id and name are kept, and nothing else is stored.
const s = lib.state(params.state);
const body = lib.obj(params.body);
let flows = lib.obj(params.flows);
const L = s.list;
const kind = L.kind || 'flows';
s.notes = [];
if (flows.source !== L.source) { flows = { source: L.source, items: [], complete: false }; }
const holder = kind === 'flows' ? flows : (kind === 'applications' ? (flows.apps = flows.apps || {}) : (flows.pol = flows.pol || {}));
if (L.pages === 0) { holder.items = []; holder.complete = false; holder.appId = kind === 'policies' ? L.appId : undefined; }
if (!lib.ok2xx(params.statusCode)) {
  lib.note(s, 'bad', 'PingOne did not return the ' + (kind === 'flows' ? 'flow' : (kind === 'applications' ? 'application' : 'flow policy')) + ' list (HTTP ' + (Number(params.statusCode) || 0) + '). ' + lib.httpMessage(body));
  return lib.out(s, 'render', { flows: { source: '', items: [], complete: false } });
}
const embedded = lib.obj(body._embedded);
const keys = kind === 'flows' ? ['flows'] : (kind === 'applications' ? ['davinciApplications', 'applications'] : ['flowPolicies']);
const page = keys.map((k) => lib.arr(embedded[k])).filter((a) => a.length)[0] || [];
for (const f of page) {
  const id = lib.str(f && (kind === 'flows' ? (f.flowId || f.id) : f.id));
  if (/^[A-Za-z0-9_-]{1,64}$/.test(id)) {
    const name = String(f && typeof f.name === 'string' ? f.name : '').replace(/[\u0000-\u001f\u007f]+/g, ' ').trim().slice(0, 200);
    holder.items.push({ id: id, name: name || id });
  }
}
L.pages += 1;
const next = lib.nextLink(body, s.cfg.apiOrigin);
if (next && L.pages < 20) {
  s.url = next;
  return lib.out(s, 'list', { flows: flows });
}
if (next) { lib.note(s, 'warn', 'The source env has more ' + (kind === 'flows' ? 'flows' : (kind === 'applications' ? 'applications' : 'flow policies')) + ' than the app reads (20 pages). Search by ID if one is missing.'); }
holder.complete = true;
if (kind === 'flows') {
  const m = lib.searchFlows(flows.items, s.search.query);
  s.search.matches = m.matches;
  s.search.total = m.total;
  s.search.listed = flows.items.length;
} else {
  const m = lib.searchFlows(holder.items, L.query);
  const target = kind === 'applications' ? s.extras.apps : s.extras.pol;
  target.matches = m.matches;
  target.total = m.total;
  target.listed = holder.items.length;
}
return lib.out(s, 'render', { flows: flows });
