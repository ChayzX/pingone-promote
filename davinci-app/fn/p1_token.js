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
const code = Number(params.statusCode) || 0;
// 400 and 401 from the token endpoint are almost always the credential's shape, not the worker's role.
const hint = code === 400 || code === 401 ? ' Check ppWorkerBasicAuth: it must be base64 of clientId:clientSecret, on one line, with nothing before it (not "Basic "). ' +
  'The worker app\'s token endpoint authentication method must be Client Secret Basic, and ppWorkerEnvironmentId must be the env that holds the worker.'
  : ' Ask your DaVinci administrator to check the worker credentials.';
lib.note(s, 'bad', 'PingOne did not let the app read ' + (THINGS[s.list.kind] || 'flows') + ' (HTTP ' + code + ').' + hint + ' ' + lib.httpMessage(body));
if (s.list.forExclude && s.exc) { if (s.list.kind === 'policies') { s.exc.pol = null; } else { s.exc.res = null; } }
else if (s.list.kind === 'applications' && s.extras) { s.extras.apps = null; } else if (s.list.kind === 'policies' && s.extras) { s.extras.pol = null; } else if (s.search) { s.search.matches = null; }
return lib.out(s, 'render');
