// After the worker token call. The token itself stays in the REST node's
// output (later REST nodes read it from there); it never enters the state.
const s = lib.state(params.state);
const e = lib.enter(s);
const body = lib.parse(params.body);
lib.call(s, 'auth', e.since, params.statusCode, lib.size(body), 29);
let action = 'finish';
const ok = lib.ok2xx(params.statusCode) && body && typeof body.access_token === 'string' && body.access_token.length > 10;
if (ok) {
  lib.end(s, 'auth', 'ok', e.t0);
  s.tokenExpiresIn = typeof body.expires_in === 'number' ? body.expires_in : 0;
  lib.begin(s, 'inventory', e.t0);
  s.inv = { k: 0, pages: 0, seen: [], first: '', flowList: [] };
  s.url = lib.inventoryUrl(s, 0);
  s.inv.first = s.url;
  s.phase = 'inventory';
  action = 'inventory';
} else {
  lib.fail(s, 'auth', lib.errInfo('worker token', params.statusCode, body));
  lib.end(s, 'auth', 'error', e.t0);
}
lib.leave(s, 'auth', e.t0);
lib.track(s);
return { state: s, action: action, url: s.url, progress: 'Signed in' };
