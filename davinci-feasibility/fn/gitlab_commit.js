// Stage 5e: commit result. Optionally build the merge request (off by default).
const s = lib.state(params.state);
const e = lib.enter(s);
const body = lib.parse(params.body);
lib.call(s, 'gitlab', e.since, params.statusCode, lib.size(body), s.gl.commitBytes || 0);
let action = 'finish';
let mrBody = '';
if (lib.ok2xx(params.statusCode)) {
  s.gl.committed = true;
  if (s.openMr) {
    mrBody = JSON.stringify({ source_branch: s.branch, target_branch: s.gl.defaultBranch, remove_source_branch: true, squash: false,
      title: 'Draft: DaVinci feasibility test (' + s.gl.path + ')',
      description: 'Generated inside DaVinci by the pingone-promote feasibility test. Do not merge; delete the branch after review.' });
    s.url = lib.gitlabUrl(s, '/merge_requests');
    action = 'mr';
  } else {
    lib.end(s, 'gitlab', 'ok', e.t0);
  }
} else {
  lib.fail(s, 'gitlab', lib.errInfo('commit', params.statusCode, body));
  lib.end(s, 'gitlab', 'error', e.t0);
}
lib.leave(s, 'gitlab', e.t0);
return lib.out(s, action, 'Committed', { mrBody: mrBody });
