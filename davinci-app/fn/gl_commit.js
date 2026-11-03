// After the Commits API call that wrote .pingone-promote/request.json.
// 2xx: the branch push starts the request job; wait for it on the working page.
// 4xx: GitLab refused, nothing was written. 5xx or no answer: unknown.
const s = lib.state(params.state);
const body = lib.obj(params.body);
const status = Number(params.statusCode) || 0;
const run = s.run;
s.notes = [];
const id = lib.str(body.id);
if (lib.ok2xx(status) && lib.SHA.test(id)) {
  run.sha = id;
  s.req.lastCommit = id;
  s.screen = 'working';
  return lib.out(s, 'render');
}
const msg = lib.httpMessage(body);
const refused = status >= 400 && status <= 499 && status !== 408 && status !== 429;
if (refused) {
  // A changed file or an existing branch is a conflict; anything else is a refusal.
  const conflict = status === 409 || /already exists|changed since|has been modified|last_commit_id|does not exist/i.test(msg);
  const err = { code: conflict ? 'conflict' : 'refused', message: 'GitLab did not accept the request (HTTP ' + status + '). ' + msg };
  if (run.kind === 'submit') { s.sub = { state: 'error', key: run.key, error: err }; }
  else { s.preview = { key: run.key, failed: true, error: err, at: lib.now() }; }
  s.screen = 'options';
  return lib.out(s, 'render');
}
// The commit may or may not exist. Never guess: Check GitLab resolves it.
if (run.kind === 'submit') { s.sub = { state: 'unknown', key: run.key }; }
else { s.preview = { key: run.key, failed: true, unknown: true, at: lib.now() }; s.sub = { state: 'unknown', key: run.key }; }
lib.note(s, 'warn', 'GitLab did not confirm the commit (' + (status ? 'HTTP ' + status : 'no answer') + '). It may still have been written. Use Check GitLab before you try again.');
s.screen = 'options';
return lib.out(s, 'render');
