// Keep one resource's promotion-variable declaration (200), its absence (404)
// or the HTTP status that stopped the read, then move to the next resource.
// Advisory: an unreadable declaration is reported, never fatal.
const ids = Array.isArray(core.parseBody(params.ids)) ? core.parseBody(params.ids) : [];
const found = Array.isArray(core.parseBody(params.found)) ? core.parseBody(params.found) : [];
const index = Number(params.index || 0);
const status = Number(params.statusCode);
const body = core.parseBody(params.body);
found.push({ resourceId: String(ids[index] || ''), status: isFinite(status) ? status : 0, body: status === 200 ? body : null });
const next = index + 1;
if (next < ids.length && next < 10000) {
  return { state: 'more', error: '', found: found, index: next, nextId: String(ids[next]), pause: next % 5 === 0 ? 'true' : 'false' };
}
return { state: 'done', error: '', found: found, index: next, nextId: '', pause: 'false' };
