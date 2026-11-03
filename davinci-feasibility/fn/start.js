// Start page submitted: check settings and run choices, start the clock.
const t0 = lib.now();
const s = lib.state({});
s.v = lib.VERSION;
s.startTs = t0;
s.runAt = new Date(t0).toISOString();
lib.segment(s, t0);
const r = lib.checkSettings(params);
const levels = { N1: 1, N5: 5, N20: 20, ALL: 0 };
const button = lib.str(params.buttonValue).toUpperCase();
const target = lib.str(params.targetName) || 'feasibility';
const branch = lib.str(params.branchName);
const out = (action) => {
  s.action = action;
  lib.leave(s, '', t0);
  lib.track(s);
  return { state: s, action: action, url: s.url || '', progress: 'Starting' };
};
s.level = Object.prototype.hasOwnProperty.call(levels, button) ? button.replace('N', '') : '';
s.requested = levels[button];
s.handBackEvery = lib.int(params.handBackEvery, 10, 0, 1000);
s.target = target;
s.openMr = /^(y|yes|true|1|on)$/i.test(lib.str(params.openMr));
s.gitlab = branch !== '';
s.branch = branch;
s.settings = { flowTimeoutSeconds: r.cfg.flowTimeoutSeconds, httpTimeoutSeconds: r.cfg.httpTimeoutSeconds, calibrationLoops: r.cfg.calibrationLoops,
  handBackEvery: s.handBackEvery, gitlabWrite: s.gitlab, openMr: s.openMr };
const problems = r.problems.map((p) => lib.varName(p.name) + ': ' + p.issue);
if (!s.level) { problems.push('Choose a load level: 1, 5, 20 or all flows.'); }
if (!/^[a-z0-9][a-z0-9_-]{0,62}$/.test(target)) { problems.push('Target name must be lowercase letters, digits, _ and - (it names environments/<target>.tfvars).'); }
if (s.gitlab) {
  const b = lib.checkBranch(branch);
  if (b) { problems.push(b); }
  if (!r.cfg.gitlabApi || !r.cfg.gitlabProjectId) { problems.push('A branch was given but fsGitlabApi or fsGitlabProjectId is not set.'); }
}
if (s.openMr && !s.gitlab) { problems.push('Opening a merge request needs a branch name.'); }
if (problems.length) {
  for (const p of problems) { lib.fail(s, 'auth', { where: 'start', status: 0, code: 'INVALID_INPUT', message: lib.redact(p) }); }
  s.invalid = true;
  return out('finish');
}
// Kept for building URLs; never copied into results or screens.
s.cfg = r.cfg;
s.url = r.cfg.authOrigin + '/' + r.cfg.workerEnvironmentId + '/as/token';
return out('ok');
