// After DELETE /repository/branches/<request branch> (Cancel).
const s = lib.state(params.state);
const status = Number(params.statusCode) || 0;
const c = s.cancel || {};
s.notes = [];
s.cancel = null;
if (lib.ok2xx(status) || status === 404) {
  lib.note(s, 'ok', 'Request canceled: its branch ' + c.branch + ' was deleted. Nothing reached the rollout.');
  if (s.req && s.req.branch === c.branch) { s.req = null; s.preview = null; s.run = null; s.sub = { state: 'idle' }; }
  if (s.requests) { s.requests.branches = (s.requests.branches || []).filter((b) => b.name !== c.branch); }
  s.screen = c.from === 'requests' ? 'requests' : 'search';
  return lib.out(s, 'render');
}
lib.note(s, 'bad', 'GitLab did not delete the branch (HTTP ' + status + '). ' + lib.httpMessage(params.body));
return lib.out(s, 'render');
