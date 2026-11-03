// After the worker token call. The token stays in the REST node's output (the
// list call reads it from there); it never enters the state or a screen.
const s = lib.state(params.state);
const body = lib.parse(params.body);
s.notes = [];
if (lib.ok2xx(params.statusCode) && body && typeof body.access_token === 'string' && body.access_token.length > 10) {
  s.url = lib.flowsUrl(s.cfg, s.list.source);
  return lib.out(s, 'list');
}
lib.note(s, 'bad', 'PingOne did not let the app read flows (HTTP ' + (Number(params.statusCode) || 0) + '). Ask your DaVinci administrator to check the worker credentials. ' + lib.httpMessage(body));
s.search.matches = null;
return lib.out(s, 'render');
