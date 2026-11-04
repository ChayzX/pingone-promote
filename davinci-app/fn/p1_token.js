// After the worker token call. The token stays in the REST node's output (the
// list call reads it from there); it never enters the state or a screen.
const s = lib.state(params.state);
const body = lib.parse(params.body);
s.notes = [];
if (lib.ok2xx(params.statusCode) && body && typeof body.access_token === 'string' && body.access_token.length > 10) {
  s.url = lib.listUrl(s.cfg, s.list);
  return lib.out(s, 'list');
}
const THINGS = { applications: 'applications', policies: 'flow policies', connections: 'connections', variables: 'variables' };
lib.note(s, 'bad', 'PingOne did not let the app read ' + (THINGS[s.list.kind] || 'flows') + ' (HTTP ' + (Number(params.statusCode) || 0) + '). Ask your DaVinci administrator to check the worker credentials. ' + lib.httpMessage(body));
if (s.list.forExclude && s.exc) { if (s.list.kind === 'policies') { s.exc.pol = null; } else { s.exc.res = null; } }
else if (s.list.kind === 'applications' && s.extras) { s.extras.apps = null; } else if (s.list.kind === 'policies' && s.extras) { s.extras.pol = null; } else if (s.search) { s.search.matches = null; }
return lib.out(s, 'render');
