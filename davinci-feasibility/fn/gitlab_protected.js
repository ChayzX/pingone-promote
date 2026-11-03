// Stage 5c: refuse a branch that matches a protected-branch name or pattern.
// If the token may not read the list (403), the name checks still apply.
const s = lib.state(params.state);
const e = lib.enter(s);
const body = lib.parse(params.body);
lib.call(s, 'gitlab', e.since, params.statusCode, lib.size(body), 0);
const st = Number(params.statusCode);
let action = 'ok';
if (lib.ok2xx(st) && Array.isArray(body)) {
  const hit = body.some((p) => p && typeof p.name === 'string' && lib.matchesPattern(p.name, s.branch));
  if (hit) {
    action = 'finish';
    lib.fail(s, 'gitlab', { where: 'protected branches', status: 0, code: 'REFUSED', message: 'branch matches a protected branch rule' });
    lib.end(s, 'gitlab', 'refused', e.t0);
  }
} else if (st === 403) {
  lib.note(s, 'The GitLab token cannot list protected branches; only the name checks guarded the branch.');
} else {
  action = 'finish';
  lib.fail(s, 'gitlab', lib.errInfo('protected branches', st, body));
  lib.end(s, 'gitlab', 'error', e.t0);
}
if (action === 'ok') {
  s.url = lib.gitlabUrl(s, '/repository/files/' + lib.encSeg(s.gl.path) + '?ref=' + lib.encSeg(s.gl.defaultBranch));
}
lib.leave(s, 'gitlab', e.t0);
return lib.out(s, action, 'Checking file');
