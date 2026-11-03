// Stage 5a: the test project must be private and not archived; learn its
// default branch and refuse to write to it.
const s = lib.state(params.state);
const e = lib.enter(s);
const body = lib.parse(params.body);
lib.call(s, 'gitlab', e.since, params.statusCode, lib.size(body), 0);
const stop = (status, info) => {
  lib.fail(s, 'gitlab', info);
  lib.end(s, 'gitlab', status, e.t0);
  lib.leave(s, 'gitlab', e.t0);
  return lib.out(s, 'finish', 'GitLab stopped');
};
if (!lib.ok2xx(params.statusCode) || !body || typeof body !== 'object') { return stop('error', lib.errInfo('read project', params.statusCode, body)); }
if (body.visibility !== 'private') { return stop('refused', { where: 'read project', status: 0, code: 'REFUSED', message: 'project is not private; use a disposable private test project' }); }
if (body.archived === true) { return stop('refused', { where: 'read project', status: 0, code: 'REFUSED', message: 'project is archived' }); }
const def = typeof body.default_branch === 'string' ? body.default_branch : '';
if (!def) { return stop('refused', { where: 'read project', status: 0, code: 'REFUSED', message: 'project has no default branch to start from' }); }
if (def.toLowerCase() === s.branch.toLowerCase()) { return stop('refused', { where: 'branch check', status: 0, code: 'REFUSED', message: 'the branch is the default branch' }); }
s.gl.defaultBranch = def;
s.url = lib.gitlabUrl(s, '/repository/branches/' + lib.encSeg(s.branch));
lib.leave(s, 'gitlab', e.t0);
return lib.out(s, 'ok', 'Checking branch');
