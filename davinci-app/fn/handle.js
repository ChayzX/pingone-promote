// A screen was submitted: apply its form, then decide the next step.
// Actions: render | token (PingOne flow list) | commit (request file) | read (GitLab queue) | signout.
const s = lib.state(params.state);
const flows = lib.obj(params.flows);
const form = lib.obj(params.form);
const btn = lib.str(params.buttonValue).toUpperCase();
const now = lib.now();
s.notes = [];
if (!s.cfg || (!s.user && s.cfg.signIn !== 'none')) { return lib.out(s, 'signout', { body: '' }); }
const cfg = s.cfg;
const T = (v) => lib.str(v).replace(/[\u0000-\u0008\u000b\u000c\u000e-\u001f\u007f]/g, '');
const done = (action, extra) => lib.out(s, action, Object.assign({ body: '' }, extra || {}));
const render = (screen) => { if (screen) { s.screen = screen; } return done('render'); };
// Sign-in is off: the first screen asks for a name. It is recorded as "<name> (unverified)" and nothing checks it.
if (!s.user) {
  if (btn === 'SIGNOUT') { s.screen = 'signedout'; return done('signout'); }
  const name = T(form.name).replace(/[{}<>"\\]/g, '').replace(/\s+/g, ' ').trim();
  s.ident = name.slice(0, 80);
  s.errors = {};
  if (btn === 'IDENTIFY') {
    if (!name) { s.errors.name = 'Type your name.'; }
    else if (name.length > 80) { s.errors.name = 'Use at most 80 characters.'; }
    else {
      s.user = { id: '', username: name + ' (unverified)', name: name, unverified: true };
      delete s.ident;
      return render('search');
    }
  }
  return render('identify');
}
// An env value is text, or JSON when it starts with [ or { (lists such as redirect URIs).
const envValue = (v) => {
  if (v && typeof v === 'object') { return v; }
  const t = T(v);
  if (/^[\[{]/.test(t)) { try { return JSON.parse(t); } catch (e) { return t; } }
  return t;
};

// The request as the options say it (Preview sets dryRun). Whole-environment requests carry scope all and confirmAll, and no flow.
const buildReq = (dryRun) => contract.buildRequest({ requestId: s.req.requestId, requestedBy: s.user.username, source: s.req.source, scope: s.req.scope,
  confirmAll: s.req.confirmAll, flow: s.req.flow, options: s.req.options, dryRun: dryRun });
// The addresses of the selected item in the last preview: the job refuses to exclude them.
const knownOf = () => ({ selected: ((s.preview && s.preview.result && s.preview.result.changes) || []).filter((c) => c.role === 'selected').map((c) => c.address) });

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
    // A resource Git does not have is added anyway; only leaving it out is a choice worth sending.
    const row = r.changes.filter((c) => c.address === a)[0];
    if (contract.CHOICES.indexOf(v) >= 0 && !(v === 'update' && row && (row.action === 'added' || (row.action === 'excluded' && !row.file)))) { deps[a] = v; }
  }
  o.deps = deps;
  // Same in all environments (inlineValues): the source value becomes a literal everywhere, so nothing is asked per env.
  const fi = lib.obj(form.inline);
  o.inline = contract.inlineChoices(r, o).filter((v) => (lib.has(fi, v) ? fi[v] === true : o.inline.indexOf(v) >= 0));
  // Environment values: the rollout envs and the source (the job accepts no other env).
  const env = {};
  const fe = lib.obj(form.env);
  for (const e of [s.req.source].concat(cfg.rollout)) {
    const vals = Object.assign({}, o.env[e] || {}, lib.obj(fe[e]));
    for (const k of Object.keys(vals)) {
      if (o.inline.indexOf(k) >= 0) { continue; }
      const v = envValue(vals[k]);
      if (v === '') { continue; }
      // The source env's ID of an excluded dependency is prefilled from the export; only a changed value is sent.
      if (e === s.req.source && r && r.changes.some((c) => c.var === k && c.sourceId === v)) { continue; }
      if (lib.looksSecret(JSON.stringify(v))) { errors['envValues.' + e + '.' + k] = 'This looks like a secret. Secrets come from Vault through customer_secrets.yml, not from here.'; continue; }
      (env[e] = env[e] || {})[k] = v;
    }
  }
  o.env = contract.withoutInlined(env, o.inline);
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
  const req = buildReq(true);
  for (const p of contract.validateRequest(req, cfg, knownOf())) { if (!errors[p.field]) { errors[p.field] = p.message; } }
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
  const req = buildReq(dryRun);
  const problems = contract.validateRequest(req, cfg, knownOf());
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
  s.req = null; s.preview = null; s.run = null; s.sub = { state: 'idle' }; s.errors = {}; s.extras = null; s.exc = null;
  return render('search');
}
if (btn === 'NAV_REQUESTS') { s.screen = 'requests'; return done(lib.queue(s, 'requests', [{ k: 'mrList' }, { k: 'requestBranches' }])); }

switch (s.screen) {
  case 'search': {
    // What to bring into Git: the items you search for (default), or the whole source env (needs a tick).
    if (btn.indexOf('SCOPE:') === 0) {
      const source = T(form.source).slice(0, 63);
      s.search = Object.assign({ matches: null, total: 0, query: '' }, s.search || {}, { scope: btn === 'SCOPE:ALL' ? 'all' : 'selected' });
      if (lib.has(cfg.sources, source)) { s.search.source = source; }
      s.errors = {};
      return render();
    }
    if (btn === 'START_ALL') {
      const source = T(form.source).slice(0, 63);
      if (!lib.has(cfg.sources, source)) { lib.note(s, 'bad', 'Choose a source env.'); return render(); }
      s.search = Object.assign({ matches: null, total: 0, query: '' }, s.search || {}, { scope: 'all', source: source });
      if (form.confirmAll !== true) { s.errors = { confirmAll: 'Tick the box to confirm: this brings the whole ' + source + ' environment into Git.' }; return render(); }
      const id = contract.newRequestId(now, Math.random().toString(36).slice(2, 8));
      const title = 'PingOne Promote: whole ' + source + ' environment';
      s.req = { requestId: id, branch: contract.branchFor(id), source: source, scope: 'all', confirmAll: true, flow: null, lastCommit: '',
        options: { mode: 'mr', title: title, commitMessage: title, description: '', deps: {}, env: {}, secrets: {}, inline: [], applications: [], flowPolicies: [], exclude: [], confirmed: {} } };
      s.preview = null; s.run = null; s.sub = { state: 'idle' }; s.errors = {}; s.extras = null; s.exc = null;
      return render('options');
    }
    if (btn === 'SEARCH') {
      const source = T(form.source).slice(0, 63);
      const query = T(form.query).trim().slice(0, 200);
      if (!lib.has(cfg.sources, source)) { lib.note(s, 'bad', 'Choose a source env.'); return render(); }
      s.search = { source: source, query: query, matches: null, total: 0, scope: 'selected' };
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
      s.req = { requestId: id, branch: contract.branchFor(id), source: s.search.source, scope: 'selected', flow: { id: f.id, name: f.name }, lastCommit: '',
        options: { mode: 'mr', title: title.slice(0, contract.LIMITS.title), commitMessage: title.slice(0, contract.LIMITS.title), description: '', deps: {}, env: {}, secrets: {},
          inline: [], applications: [], flowPolicies: [], exclude: [], confirmed: {} } };
      s.preview = null; s.run = null; s.sub = { state: 'idle' }; s.errors = {}; s.extras = null; s.exc = null;
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
    if (btn === 'EXTRAS' && cfg.pathApplications && s.req && s.req.scope !== 'all') { s.extras = s.extras || { apps: null, pol: null }; s.errors = {}; return render('extras'); }
    if (btn === 'EXCLUDE' && s.req) { s.exc = s.exc || { kind: 'flow', query: '', res: null, pol: null }; s.errors = {}; return render('exclude'); }
    if (btn === 'BACK') { return render('search'); }
    if (btn === 'CHECK' && s.req) { return done(lib.queue(s, 'check', [{ k: 'requestFile', branch: s.req.branch }])); }
    if (btn === 'CANCEL' && s.req && s.req.lastCommit && s.sub.state !== 'done') {
      s.cancel = { branch: s.req.branch, sha: s.req.lastCommit, from: 'request' };
      return done(lib.queue(s, 'cancel', [{ k: 'branch', branch: s.req.branch }]));
    }
    return render();
  }
  case 'extras': {
    // Optional applications and flow policies, found by name in the source env (read-only, like the flow search).
    // Applications: 1 token + the list pages. Flow policies: PingOne lists them per application, so only the
    // policies of the one application the person opens are read (1 token + their pages). Everything else is free.
    if (!s.req || !cfg.pathApplications) { return render('options'); }
    const ex = s.extras = s.extras || { apps: null, pol: null };
    const o = s.req.options;
    const startList = (kind, more) => { s.list = Object.assign({ kind: kind, source: s.req.source, pages: 0 }, more); s.url = lib.tokenUrl(cfg); return done('token'); };
    const cached = (bucket, appId) => flows.source === s.req.source && bucket && bucket.complete && (appId === undefined || bucket.appId === appId);
    const fill = (target, bucket, query) => { const m = lib.searchFlows(bucket.items, query); target.matches = m.matches; target.total = m.total; target.listed = bucket.items.length; };
    // A name that two listed items share cannot be told apart: the job selects applications and flow policies by name only.
    const add = (kind, what, name, bucket, extra) => {
      if (o[kind].some((x) => x.name === name)) { lib.note(s, 'info', name + ' is already in the request.'); return; }
      if (o[kind].length >= contract.LIMITS.refs) { lib.note(s, 'bad', 'A request can hold at most ' + contract.LIMITS.refs + ' ' + what + 's.'); return; }
      if (bucket && bucket.items.filter((x) => x.name.toLowerCase() === name.toLowerCase()).length > 1) {
        lib.note(s, 'bad', 'Two ' + what + 's are named “' + name + '”. The request job selects by name, so it could not tell them apart. Rename one in the source env first.');
        return;
      }
      o[kind].push(Object.assign({ name: name }, extra || {}));
    };
    if (btn === 'EXTRAS_DONE') { s.errors = {}; return render('options'); }
    if (btn === 'SEARCH_APP') {
      const query = T(form.appQuery).trim().slice(0, 200);
      ex.apps = { query: query, matches: null, total: 0 };
      ex.pol = null;
      if (!query) { s.errors = { appQuery: 'Type part of an application name, or its ID.' }; return render(); }
      s.errors = {};
      if (cached(flows.apps)) { fill(ex.apps, flows.apps, query); return render(); }
      return startList('applications', { query: query });
    }
    if (btn.indexOf('POLICIES:') === 0 && cfg.pathFlowPolicies) {
      const a = pickIndex('POLICIES:', ex.apps && ex.apps.matches);
      if (!a) { return render(); }
      ex.pol = { app: { id: a.id, name: a.name }, query: '', matches: null, total: 0 };
      s.errors = {};
      if (cached(flows.pol, a.id)) { fill(ex.pol, flows.pol, ''); return render(); }
      return startList('policies', { appId: a.id, query: '' });
    }
    if (btn === 'SEARCH_POL' && ex.pol) {
      const query = T(form.polQuery).trim().slice(0, 200);
      ex.pol.query = query;
      s.errors = {};
      if (cached(flows.pol, ex.pol.app.id)) { fill(ex.pol, flows.pol, query); return render(); }
      return startList('policies', { appId: ex.pol.app.id, query: query });
    }
    if (btn.indexOf('ADDAPP:') === 0) {
      const a = pickIndex('ADDAPP:', ex.apps && ex.apps.matches);
      if (a) { add('applications', 'application', a.name, flows.apps); }
      return render();
    }
    if (btn.indexOf('ADDPOL:') === 0 && ex.pol) {
      const m = pickIndex('ADDPOL:', ex.pol.matches);
      if (m) { add('flowPolicies', 'flow policy', m.name, flows.pol, { app: ex.pol.app.name }); }
      return render();
    }
    if (btn.indexOf('DROPAPP:') === 0) { const x = pickIndex('DROPAPP:', o.applications); if (x) { o.applications = o.applications.filter((y) => y !== x); } return render(); }
    if (btn.indexOf('DROPPOL:') === 0) { const x = pickIndex('DROPPOL:', o.flowPolicies); if (x) { o.flowPolicies = o.flowPolicies.filter((y) => y !== x); } return render(); }
    return render();
  }
  case 'exclude': {
    // Items to leave out, found by name in the source env like an include is: none, several (listed), or one. Own step, own REST budget:
    // a first search of a kind reads 1 token + its list pages; everything after that is free. Flow policies are read per application.
    if (!s.req) { return render('search'); }
    const ex = s.exc = s.exc || { kind: 'flow', query: '', res: null, pol: null };
    const o = s.req.options;
    const kinds = { flow: true, application: true, connection: !!cfg.pathConnections, variable: !!cfg.pathVariables };
    const listKind = (k) => contract.EXCLUDE_KINDS[k].list;
    const startList = (kind, more) => { s.list = Object.assign({ kind: kind, source: s.req.source, pages: 0, forExclude: true }, more); s.url = lib.tokenUrl(cfg); return done('token'); };
    const cached = (bucket, appId) => flows.source === s.req.source && bucket && bucket.complete && (appId === undefined || bucket.appId === appId);
    const bucketOf = (kind) => (kind === 'flow' ? flows : (kind === 'application' ? flows.apps : (kind === 'connection' ? flows.conns : flows.vars)));
    const fill = (target, bucket, query) => { const m = lib.searchFlows(bucket.items, query); target.matches = m.matches; target.total = m.total; target.listed = bucket.items.length; };
    // The job finds a resource by name (a flow also by ID), so two listed items with one name cannot be told apart.
    const add = (type, what, item, bucket, label) => {
      const name = type === 'variable' ? item.name + '_' + item.ctx : item.name;
      const show = type === 'variable' ? item.name + ' (' + item.ctx + ')' : item.name;
      if (o.exclude.some((x) => x.type === type && (x.name === name || (x.id && x.id === item.id)))) { lib.note(s, 'info', show + ' is already excluded.'); return; }
      if (o.exclude.length >= contract.LIMITS.excludes) { lib.note(s, 'bad', 'A request can exclude at most ' + contract.LIMITS.excludes + ' items.'); return; }
      if (type === 'flow' && s.req.flow && (s.req.flow.id === item.id || s.req.flow.name.toLowerCase() === item.name.toLowerCase())) {
        lib.note(s, 'bad', item.name + ' is the main flow of this request; a request cannot include and exclude the same flow.');
        return;
      }
      if (type === 'application' && o.applications.some((x) => x.name.toLowerCase() === item.name.toLowerCase())) {
        lib.note(s, 'bad', item.name + ' is included in this request. Remove it from the included applications first.');
        return;
      }
      if (type === 'flowPolicy' && o.flowPolicies.some((x) => x.name.toLowerCase() === item.name.toLowerCase())) {
        lib.note(s, 'bad', item.name + ' is included in this request. Remove it from the included flow policies first.');
        return;
      }
      if (type !== 'flow' && bucket && bucket.items.filter((x) => x.name.toLowerCase() === item.name.toLowerCase() && (type !== 'variable' || x.ctx === item.ctx)).length > 1) {
        lib.note(s, 'bad', 'Two ' + what + 's are named “' + show + '”. The request job selects by name, so it could not tell them apart. Rename one in the source env first.');
        return;
      }
      o.exclude.push(Object.assign({ type: type, name: name, show: label ? show + ' (application ' + label + ')' : show }, type === 'flow' ? { id: item.id } : {}));
    };
    if (btn === 'EXC_DONE') { s.errors = {}; return render('options'); }
    if (btn === 'EXC_SEARCH') {
      const kind = T(form.excKind);
      ex.kind = lib.has(kinds, kind) && kinds[kind] ? kind : ex.kind;
      const query = T(form.excQuery).trim().slice(0, 200);
      ex.query = query;
      ex.res = { kind: ex.kind, query: query, matches: null, total: 0 };
      ex.pol = null;
      if (!query) { s.errors = { excQuery: 'Type part of a name' + (ex.kind === 'flow' ? ', or a flow ID.' : '.') }; ex.res = null; return render(); }
      s.errors = {};
      const bucket = bucketOf(ex.kind);
      if (cached(bucket)) { fill(ex.res, bucket, query); return render(); }
      return startList(listKind(ex.kind), { query: query });
    }
    if (btn.indexOf('EXC_POLICIES:') === 0 && cfg.pathFlowPolicies) {
      const a = pickIndex('EXC_POLICIES:', ex.res && ex.res.kind === 'application' && ex.res.matches);
      if (!a) { return render(); }
      ex.pol = { app: { id: a.id, name: a.name }, query: '', matches: null, total: 0 };
      s.errors = {};
      if (cached(flows.pol, a.id)) { fill(ex.pol, flows.pol, ''); return render(); }
      return startList('policies', { appId: a.id, query: '' });
    }
    if (btn === 'EXC_SEARCH_POL' && ex.pol) {
      const query = T(form.excPolQuery).trim().slice(0, 200);
      ex.pol.query = query;
      s.errors = {};
      if (cached(flows.pol, ex.pol.app.id)) { fill(ex.pol, flows.pol, query); return render(); }
      return startList('policies', { appId: ex.pol.app.id, query: query });
    }
    if (btn.indexOf('EXC_ADD:') === 0) {
      const m = pickIndex('EXC_ADD:', ex.res && ex.res.matches);
      if (m) { add(ex.res.kind, contract.EXCLUDE_KINDS[ex.res.kind].label, m, bucketOf(ex.res.kind)); }
      return render();
    }
    if (btn.indexOf('EXC_ADDPOL:') === 0 && ex.pol) {
      const m = pickIndex('EXC_ADDPOL:', ex.pol.matches);
      if (m) { add('flowPolicy', 'flow policy', m, flows.pol, ex.pol.app.name); }
      return render();
    }
    if (btn.indexOf('EXC_DROP:') === 0) { const x = pickIndex('EXC_DROP:', o.exclude); if (x) { o.exclude = o.exclude.filter((y) => y !== x); } return render(); }
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
