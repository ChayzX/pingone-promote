// Stage 5d: does the file exist on the default branch? Build the Commits API
// body: one action on the NEW branch, started from the default branch.
const s = lib.state(params.state);
const e = lib.enter(s);
const st = Number(params.statusCode);
lib.call(s, 'gitlab', e.since, st, 0, 0);
let action = 'ok';
let commitBody = '';
if (st === 200 || st === 404) {
  s.gl.fileAction = st === 200 ? 'update' : 'create';
  commitBody = JSON.stringify({
    branch: s.branch,
    start_branch: s.gl.defaultBranch,
    commit_message: 'DaVinci feasibility test: ' + s.gl.path + ' (' + (s.level === 'ALL' ? 'all' : s.level) + ' flows)',
    actions: [{ action: s.gl.fileAction, file_path: s.gl.path, content: String(params.tfvars || '') }]
  });
  s.gl.commitBytes = lib.utf8Len(commitBody);
  s.url = lib.gitlabUrl(s, '/repository/commits');
} else {
  action = 'finish';
  lib.fail(s, 'gitlab', lib.errInfo('file check', st, params.body));
  lib.end(s, 'gitlab', 'error', e.t0);
}
lib.leave(s, 'gitlab', e.t0);
return lib.out(s, action, 'Committing', { commitBody: commitBody });
