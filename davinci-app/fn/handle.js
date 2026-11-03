// A screen was submitted: apply its form, then decide the next step.
// Actions: render | token (PingOne flow list) | commit (request file) | read (GitLab queue) | signout.
const s = lib.state(params.state);
const flows = lib.obj(params.flows);
const form = lib.obj(params.form);
const btn = lib.str(params.buttonValue).toUpperCase();
const now = lib.now();
s.notes = [];
if (!s.user || !s.cfg) { return lib.out(s, 'signout', { body: '' }); }
const cfg = s.cfg;
const T = (v) => lib.str(v).replace(/[\u0000-\u0008\u000b\u000c\u000e-\u001f\u007f]/g, '');
const done = (action, extra) => lib.out(s, action, Object.assign({ body: '' }, extra || {}));
const render = (screen) => { if (screen) { s.screen = screen; } return done('render'); };
// An env value is text, or JSON when it starts with [ or { (lists such as redirect URIs).
const envValue = (v) => {
  if (v && typeof v === 'object') { return v; }
  const t = T(v);
  if (/^[\[{]/.test(t)) { try { return JSON.parse(t); } catch (e) { return t; } }
  return t;
};

// ---- options form -------------------------------------------------------------------
// Error keys are the request job's field paths (title, envValues.<env>.<var>, secretMappings.<key>, ...).
const applyOptions = () => {
  if (s.screen !== 'options' || !s.req) { return; }
  const o = s.req.options;
  const before = lib.optionsKey(o);
  const errors = {};
  const r = s.preview && s.preview.result;
  const mode = T(form.mode);
  if (contract.MODES.indexOf(mode) >= 0) { o.mode = mode; }
  if (lib.has(form, 'title')) { o.title = T(form.title).trim(); }
  if (lib.has(form, 'commitMessage')) { o.commitMessage = T(form.commitMessage).trim(); }
  if (lib.has(form, 'description')) { o.description = T(form.description); }
  if (!o.title) { errors.title = 'Give the request a title.'; }
  // Dependency choices: for dependencies the last preview listed as differing,
  // and for any address changed in both Git and the source.
  const deps = {};
  const fd = lib.obj(form.deps);
  for (const a of lib.choosable(r)) {
    const v = lib.has(fd, a) ? fd[a] : o.deps[a];
    if (contract.CHOICES.indexOf(v) >= 0) { deps[a] = v; }
  }
  o.deps = deps;
  // Environment values: the rollout envs and the source (the job accepts no other env).
  const env = {};
  const fe = lib.obj(form.env);
  for (const e of [s.req.source].concat(cfg.rollout)) {
    const vals = Object.assign({}, o.env[e] || {}, lib.obj(fe[e]));
    for (const k of Object.keys(vals)) {
      const v = envValue(vals[k]);
      if (v === '') { continue; }
      if (lib.looksSecret(JSON.stringify(v))) { errors['envValues.' + e + '.' + k] = 'This looks like a secret. Secrets come from Vault through customer_secrets.yml, not from here.'; continue; }
      (env[e] = env[e] || {})[k] = v;
    }
  }
  o.env = env;
  // Secret mappings: terraformer secret variable -> an existing customer_secrets.yml name.
  // Names only, never values. Ticks confirm a new secret's value is in Vault.
  const secrets = {};
  const confirmed = {};
  const fs = lib.obj(form.secrets);
  for (const x of (r && r.requiredSecrets) || []) {
    const f = lib.obj(fs[x.key]);
    const name = (lib.has(f, 'mapTo') ? T(f.mapTo) : lib.str((o.secrets || {})[x.key])).trim();
    if (name) { secrets[x.key] = name; }
    const tick = lib.has(f, 'ok') ? f.ok === true : !!(o.confirmed || {})[x.key];
    if (tick) { confirmed[x.key] = true; }
  }
  o.secrets = secrets;
  o.confirmed = confirmed;
  // The job's own rules, checked here first (contract.validateRequest).
  const req = contract.buildRequest({ requestId: s.req.requestId, requestedBy: s.user.username, source: s.req.source, flow: s.req.flow, options: o, dryRun: true });
  for (const p of contract.validateRequest(req, cfg)) { if (!errors[p.field]) { errors[p.field] = p.message; } }
  s.errors = errors;
  // #57: an edit clears a failed submit and re-enables Submit; an unknown outcome stays until Check GitLab.
  if (lib.optionsKey(o) !== before) {
    s.jobField = null;
    if (s.sub.state === 'error') { s.sub = { state: 'idle' }; }
  }
};

// ---- Preview / Submit: ONE Commits API call --------------------------------------------
const commit = (dryRun) => {
  const why = dryRun ? lib.canPreview(s) : lib.canSubmit(s);
  if (why) { lib.note(s, 'bad', why); return render(); }
  const req = contract.buildRequest({ requestId: s.req.requestId, requestedBy: s.user.username, source: s.req.source, flow: s.req.flow,
    options: s.req.options, dryRun: dryRun });
  const problems = contract.validateRequest(req, cfg);
  if (problems.length) { for (const p of problems) { lib.note(s, 'bad', p.field + ': ' + p.message); } return render(); }
  const body = contract.commitBody(req, cfg.defaultBranch, s.req.lastCommit || '');
  s.url = lib.gl(cfg, 'commit').url;
  s.run = { kind: dryRun ? 'preview' : 'submit', key: lib.optionsKey(s.req.options), sha: '', pipelineId: '', jobId: '', jobStatus: '',
    startedAt: now, deadline: now + cfg.runTimeoutSeconds * 1000, polls: 0, auto: true, firstCommit: !s.req.lastCommit, expect: JSON.stringify(req) };
  if (!dryRun) { s.sub = { state: 'running', key: s.run.key }; }
  return done('commit', { body: JSON.stringify(body) });
};

// ---- status and requests ------------------------------------------------------------------
const watch = (st) => {
  s.st = Object.assign({ envs: {}, polls: 0, startedAt: now, paused: false, auto: true }, st);
  s.screen = 'status';
  return lib.queue(s, 'status', [s.st.iid ? { k: 'mr', iid: s.st.iid } : { k: 'mrsBySource', branch: s.st.branch }]);
};
const pickIndex = (prefix, list) => {
  const n = Number(btn.slice(prefix.length));
  return Number.isInteger(n) && list && n >= 0 && n < list.length ? list[n] : null;
};

// ---- global navigation ----------------------------------------------------------------------
if (btn === 'SIGNOUT') { s.screen = 'signedout'; return done('signout'); }
if (btn === 'NAV_NEW') {
  if (s.req && s.req.lastCommit && s.sub.state !== 'done') { lib.note(s, 'info', 'The previous request still has a branch in GitLab. You can cancel it from Requests.'); }
  s.req = null; s.preview = null; s.run = null; s.sub = { state: 'idle' }; s.errors = {};
  return render('search');
}
if (btn === 'NAV_REQUESTS') { s.screen = 'requests'; return done(lib.queue(s, 'requests', [{ k: 'mrList' }, { k: 'requestBranches' }])); }

switch (s.screen) {
  case 'search': {
    if (btn === 'SEARCH') {
      const source = T(form.source).slice(0, 63);
      const query = T(form.query).trim().slice(0, 200);
      if (!lib.has(cfg.sources, source)) { lib.note(s, 'bad', 'Choose a source env.'); return render(); }
      s.search = { source: source, query: query, matches: null, total: 0 };
      if (!query) { s.errors = { query: 'Type part of a flow name, or a flow ID.' }; return render(); }
      s.errors = {};
      if (flows.source === source && flows.complete) {
        const m = lib.searchFlows(flows.items, query);
        s.search.matches = m.matches; s.search.total = m.total;
        return render();
      }
      s.list = { source: source, pages: 0 };
      s.url = lib.tokenUrl(cfg);
      return done('token');
    }
    if (btn.indexOf('PICK:') === 0) {
      const f = pickIndex('PICK:', s.search.matches);
      if (!f) { return render(); }
      const rnd = Math.random().toString(36).slice(2, 8);
      const id = contract.newRequestId(now, rnd);
      const title = 'PingOne Promote: ' + f.name + ' from ' + s.search.source;
      s.req = { requestId: id, branch: contract.branchFor(id), source: s.search.source, flow: { id: f.id, name: f.name }, lastCommit: '',
        options: { mode: 'mr', title: title.slice(0, contract.LIMITS.title), commitMessage: title.slice(0, contract.LIMITS.title), description: '', deps: {}, env: {}, secrets: {},
          inline: [], remove: [], confirmed: {} } };
      s.preview = null; s.run = null; s.sub = { state: 'idle' }; s.errors = {};
      return render('options');
    }
    return render();
  }
  case 'options':
  case 'preview': {
    applyOptions();
    if (btn === 'PREVIEW') { return commit(true); }
    if (btn === 'SUBMIT') { return commit(false); }
    if (btn === 'EDIT') { return render('options'); }
    if (btn === 'BACK') { return render('search'); }
    if (btn === 'CHECK' && s.req) { return done(lib.queue(s, 'check', [{ k: 'requestFile', branch: s.req.branch }])); }
    if (btn === 'CANCEL' && s.req && s.req.lastCommit && s.sub.state !== 'done') {
      s.cancel = { branch: s.req.branch, sha: s.req.lastCommit, from: 'request' };
      return done(lib.queue(s, 'cancel', [{ k: 'branch', branch: s.req.branch }]));
    }
    return render();
  }
  case 'working': {
    if (!s.run) { return render('options'); }
    if (btn === 'STOP') {
      s.run.auto = false;
      if (s.run.kind === 'submit') { s.sub = { state: 'unknown', key: s.run.key }; lib.note(s, 'warn', 'You stopped waiting. The submit may still finish in GitLab; use Check GitLab to find out.'); }
      else { lib.note(s, 'info', 'You stopped waiting for the preview.'); }
      return render('options');
    }
    if (btn === 'KEEP') { s.run.deadline = now + cfg.runTimeoutSeconds * 1000; s.run.auto = true; }
    if (btn === 'CHECK') { return done(lib.queue(s, 'check', [{ k: 'requestFile', branch: s.req.branch }])); }
    s.run.polls += 1;
    return done(lib.queue(s, 'poll', s.run.pipelineId ? [{ k: 'jobs', id: s.run.pipelineId }] : [{ k: 'branchPipelines', branch: s.req.branch, sha: s.run.sha }]));
  }
  case 'submitted': {
    if (btn === 'WATCH' && s.sub.result) {
      const iid = s.sub.result.mrIid || contract.mrIid(s.sub.result.mrUrl);
      return done(watch({ iid: iid, branch: s.req.branch, mode: iid ? 'mr' : 'branch', title: s.req.options.title }));
    }
    return render();
  }
  case 'status': {
    if (!s.st) { return render('requests'); }
    if (btn === 'PAUSE') { s.st.paused = true; return render(); }
    if (btn === 'RESUME') { s.st.paused = false; s.st.startedAt = now; s.st.auto = true; }
    s.st.polls += 1;
    return done(lib.queue(s, 'status', [s.st.iid ? { k: 'mr', iid: s.st.iid } : { k: 'mrsBySource', branch: s.st.branch }]));
  }
  case 'requests': {
    const rq = s.requests || { list: [], branches: [] };
    if (btn.indexOf('OPEN:') === 0) {
      const m = pickIndex('OPEN:', rq.list);
      if (m) { return done(watch({ iid: m.iid, branch: m.sourceBranch, mode: 'mr', title: m.title })); }
    }
    if (btn.indexOf('OPENB:') === 0) {
      const b = pickIndex('OPENB:', rq.branches);
      if (b) { return done(watch({ iid: '', branch: b.name, mode: 'branch', title: b.name })); }
    }
    if (btn.indexOf('CANCELB:') === 0) {
      const b = pickIndex('CANCELB:', rq.branches);
      if (b && b.cancellable) { s.cancel = { branch: b.name, sha: b.sha, from: 'requests' }; return done(lib.queue(s, 'cancel', [{ k: 'branch', branch: b.name }])); }
    }
    return done(lib.queue(s, 'requests', [{ k: 'mrList' }, { k: 'requestBranches' }]));
  }
  default:
    return render('search');
}
