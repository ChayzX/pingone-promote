// Collect one page of the source env's flow list (id and name only), follow
// next links, then search. The search never picks a flow by itself (#93).
const s = lib.state(params.state);
const body = lib.obj(params.body);
let flows = lib.obj(params.flows);
s.notes = [];
if (flows.source !== s.list.source || s.list.pages === 0) { flows = { source: s.list.source, items: [], complete: false }; }
if (!lib.ok2xx(params.statusCode)) {
  lib.note(s, 'bad', 'PingOne did not return the flow list (HTTP ' + (Number(params.statusCode) || 0) + '). ' + lib.httpMessage(body));
  return lib.out(s, 'render', { flows: { source: '', items: [], complete: false } });
}
const page = lib.arr(lib.obj(body._embedded).flows);
for (const f of page) {
  const id = lib.str(f && (f.flowId || f.id));
  if (/^[A-Za-z0-9_-]{1,64}$/.test(id)) { flows.items.push({ id: id, name: String(typeof f.name === 'string' ? f.name : '').trim().slice(0, 200) || id }); }
}
s.list.pages += 1;
const next = lib.nextLink(body, s.cfg.apiOrigin);
if (next && s.list.pages < 20) {
  s.url = next;
  return lib.out(s, 'list', { flows: flows });
}
if (next) { lib.note(s, 'warn', 'The source env has more flows than the app reads (20 pages). Search by flow ID if a flow is missing.'); }
flows.complete = true;
const m = lib.searchFlows(flows.items, s.search.query);
s.search.matches = m.matches;
s.search.total = m.total;
s.search.listed = flows.items.length;
return lib.out(s, 'render', { flows: flows });
