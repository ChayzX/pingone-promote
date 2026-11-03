// Stage 5b: the branch must not exist yet (404). Never overwrite a branch.
const s = lib.state(params.state);
const e = lib.enter(s);
const body = lib.parse(params.body);
lib.call(s, 'gitlab', e.since, params.statusCode, lib.size(body), 0);
const st = Number(params.statusCode);
let action = 'ok';
if (st === 404) {
  s.url = lib.gitlabUrl(s, '/protected_branches?per_page=100');
} else {
  action = 'finish';
  if (lib.ok2xx(st)) {
    lib.fail(s, 'gitlab', { where: 'branch check', status: st, code: 'REFUSED', message: 'branch already exists; choose a new name' });
    lib.end(s, 'gitlab', 'refused', e.t0);
  } else {
    lib.fail(s, 'gitlab', lib.errInfo('branch check', st, body));
    lib.end(s, 'gitlab', 'error', e.t0);
  }
}
lib.leave(s, 'gitlab', e.t0);
return lib.out(s, action, 'Checking protected branches');
