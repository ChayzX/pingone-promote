// One GitLab GET answered. Handle it by phase (poll | check | cancel | status |
// requests), queue more reads if needed, then render. At most lib.MAX_CALLS
// reads run per browser request.
// Actions: 'read' (next queued GET) | 'delete' (cancel confirmed) | 'render'.
const s = lib.state(params.state);
const status = Number(params.statusCode) || 0;
const ok = lib.ok2xx(status);
const body = lib.parse(params.body);
const cfg = s.cfg;
const q = s.q;
const item = q.todo.shift();
const now = lib.now();
const run = s.run;
const st = s.st;
q.calls += 1;
const push = (it) => { q.todo.push(it); };
const TERMINAL_JOB = ['success', 'failed', 'canceled'];
const ACTIVE_PIPE = ['created', 'waiting_for_resource', 'preparing', 'pending', 'running', 'manual', 'scheduled'];
const list = Array.isArray(body) ? body : [];
const o = body && typeof body === 'object' && !Array.isArray(body) ? body : {};
let action = 'render';
const gone = () => lib.note(s, 'bad', 'GitLab did not answer as expected (HTTP ' + status + '). ' + lib.httpMessage(body));

// ---- results of a request job ------------------------------------------------------
const finishResult = (raw) => {
  const r = contract.normalizeResult(raw);
  run.auto = false;
  run.done = true;
  // A field the job refused is pointed out on the options screen without blocking it.
  s.jobField = r.error && r.error.field ? { field: r.error.field, message: r.error.message } : null;
  if (run.kind === 'preview') {
    s.preview = { key: run.key, result: r, jobUrl: run.jobUrl, pipelineUrl: run.pipelineUrl, at: now };
    s.sub = s.sub.state === 'unknown' ? { state: 'idle' } : s.sub;
    s.screen = 'preview';
    return;
  }
  if (r.error) {
    s.sub = { state: 'error', key: run.key, error: r.error, result: r, jobUrl: run.jobUrl };
    s.screen = 'preview';
    if (!s.preview || s.preview.key !== run.key) { s.preview = { key: run.key, result: r, jobUrl: run.jobUrl, at: now }; }
    return;
  }
  // submitted, already_submitted (the existing commit and MR) and no_changes all end the request.
  r.mrUrl = lib.webUrl(cfg, r.mrUrl);
  r.branchUrl = lib.webUrl(cfg, r.branchUrl);
  r.diffUrl = lib.webUrl(cfg, r.diffUrl);
  s.sub = { state: 'done', key: run.key, result: r, jobUrl: run.jobUrl };
  s.screen = 'submitted';
};
const finishNoResult = (why) => {
  run.auto = false;
  run.done = true;
  if (run.kind === 'preview') {
    s.preview = { key: run.key, failed: true, message: why, jobUrl: run.jobUrl, pipelineUrl: run.pipelineUrl, at: now };
    s.screen = 'preview';
  } else {
    // The job may have written part of the change before it stopped.
    s.sub = { state: 'unknown', key: run.key, message: why, jobUrl: run.jobUrl };
    s.screen = 'preview';
  }
};

// ---- handlers ---------------------------------------------------------------------------
const H = {};
H['poll:branchPipelines'] = H['check:branchPipelines'] = () => {
  if (!ok) { gone(); return; }
  const p = list[0];
  if (p && lib.NUM.test(String(p.id))) {
    run.pipelineId = String(p.id);
    run.pipelineUrl = lib.webUrl(cfg, p.web_url);
    run.pipelineStatus = lib.str(p.status);
    push({ k: 'jobs', id: run.pipelineId });
  }
};
H['poll:jobs'] = H['check:jobs'] = () => {
  if (!ok) { gone(); return; }
  const jobs = list.filter((j) => j && j.name === contract.REQUEST_JOB).sort((a, b) => b.id - a.id);
  const j = jobs[0];
  if (!j) {
    const ps = lib.str(list[0] && list[0].pipeline && list[0].pipeline.status) || run.pipelineStatus;
    if (list.length && TERMINAL_JOB.indexOf(ps) >= 0) { finishNoResult('The pipeline has no ' + contract.REQUEST_JOB + ' job.'); }
    return;
  }
  run.jobId = String(j.id);
  run.jobStatus = lib.str(j.status);
  run.jobUrl = lib.webUrl(cfg, j.web_url);
  run.jobStartedAt = j.started_at ? Date.parse(j.started_at) || 0 : 0;
  if (TERMINAL_JOB.indexOf(run.jobStatus) >= 0) { push({ k: 'artifact', id: run.jobId }); }
};
H['poll:artifact'] = H['check:artifact'] = () => {
  const r = lib.parse(body);
  if (ok && r && typeof r === 'object' && !Array.isArray(r)) { finishResult(r); return; }
  finishNoResult('The ' + contract.REQUEST_JOB + ' job ended (' + run.jobStatus + ') without ' + contract.RESULT_ARTIFACT + '.');
};
// Check GitLab: find out what the last Preview or Submit actually did.
H['check:requestFile'] = () => {
  if (ok) {
    let req = null;
    try { req = JSON.parse(lib.unb64(o.content)); } catch (e) { req = null; }
    const last = lib.str(o.last_commit_id);
    if (!req || req.requestId !== s.req.requestId || !lib.SHA.test(last)) { lib.note(s, 'bad', 'The request file on the branch is not this request. Open the branch in GitLab.'); return; }
    s.req.lastCommit = last;
    if (run && JSON.stringify(req) === run.expect) {
      // The last commit landed: wait for its job again.
      run.sha = last; run.pipelineId = ''; run.jobId = ''; run.auto = true; run.done = false; run.deadline = now + cfg.runTimeoutSeconds * 1000;
      if (run.kind === 'submit') { s.sub = { state: 'running', key: run.key }; } else { s.sub = { state: 'idle' }; }
      q.phase = 'poll';
      push({ k: 'branchPipelines', branch: s.req.branch, sha: last });
      lib.note(s, 'info', 'GitLab has the request. Waiting for its job.');
      return;
    }
    s.sub = { state: 'idle' };
    if (s.preview && s.preview.unknown) { s.preview = null; }
    lib.note(s, 'info', 'Your last ' + (run && run.kind === 'submit' ? 'submit' : 'preview') + ' did not reach GitLab. Nothing changed; you can try again.');
    s.screen = 'options';
    return;
  }
  if (status === 404) { push({ k: 'branch', branch: s.req.branch }); return; }
  gone();
};
H['check:branch'] = () => {
  if (status === 404) {
    if (s.req.lastCommit && !(run && run.firstCommit)) {
      s.req.gone = true;
      lib.note(s, 'bad', 'The request branch no longer exists (it was deleted in GitLab). Start a new request.');
    } else {
      s.req.lastCommit = '';
      lib.note(s, 'info', 'GitLab has no branch for this request: nothing was written. You can try again.');
    }
    s.sub = { state: 'idle' };
    if (s.preview && s.preview.unknown) { s.preview = null; }
    s.screen = 'options';
    return;
  }
  if (!ok) { gone(); return; }
  // The branch exists but the request file is gone: the job took the submit.
  s.checkBranchUrl = lib.webUrl(cfg, o.web_url);
  push({ k: 'mrsBySource', branch: s.req.branch });
};
H['check:mrsBySource'] = () => {
  if (!ok) { gone(); return; }
  const mr = list[0];
  if (mr && lib.webUrl(cfg, mr.web_url)) {
    s.sub = { state: 'done', key: run ? run.key : '', result: contract.normalizeResult({ resultVersion: contract.RESULT_VERSION, status: 'submitted', mrUrl: mr.web_url, changes: [] }) };
    s.sub.result.mrUrl = lib.webUrl(cfg, mr.web_url);
    s.sub.found = true;
    s.screen = 'submitted';
  } else if (s.req.options.mode === 'branch') {
    s.sub = { state: 'done', key: run ? run.key : '', result: contract.normalizeResult({ resultVersion: contract.RESULT_VERSION, status: 'submitted', branchUrl: s.checkBranchUrl, changes: [] }), found: true };
    s.sub.result.branchUrl = s.checkBranchUrl;
    s.screen = 'submitted';
  } else {
    lib.note(s, 'warn', 'The job has taken the request but has not opened the merge request yet. Check again in a minute.');
  }
};
// Cancel: only while the branch head is still a request commit.
H['cancel:branch'] = () => {
  const c = s.cancel;
  if (status === 404) { lib.note(s, 'info', 'The branch ' + c.branch + ' is already gone.'); action = 'none'; s.cancel = null; return; }
  if (!ok) { gone(); s.cancel = null; return; }
  const head = lib.obj(o.commit);
  const idPart = c.branch.slice(contract.BRANCH_PREFIX.length);
  const isRequestCommit = new RegExp('^pingone-promote: (preview|submit) request ' + idPart + '$').test(lib.str(head.title));
  if (isRequestCommit && (!c.sha || head.id === c.sha)) {
    s.url = lib.gl(cfg, 'deleteBranch', { branch: c.branch }).url;
    action = 'delete';
    return;
  }
  lib.note(s, 'bad', 'Not canceled: the request job (or someone) has already committed to ' + c.branch + '. Close it in GitLab instead.');
  s.cancel = null;
};
// ---- rollout status --------------------------------------------------------------------
const envState = (env, state, detail) => { st.envs[env] = { state: state, detail: detail || '' }; };
const fromJob = (j) => {
  const m = { success: 'Live', failed: 'Failed', canceled: 'Canceled', running: 'Running', pending: 'Running', preparing: 'Running',
    waiting_for_resource: 'Running', manual: 'Needs approval', created: 'Waiting', scheduled: 'Waiting', skipped: 'Waiting' };
  return m[j.status] || 'Waiting';
};
const takeMr = (mr) => {
  st.iid = String(mr.iid);
  st.mr = { state: lib.str(mr.state), title: lib.redact(mr.title, 200), webUrl: lib.webUrl(cfg, mr.web_url), mergedAt: lib.str(mr.merged_at) };
  const sha = lib.str(mr.merge_commit_sha) || lib.str(mr.squash_commit_sha);
  for (const e of cfg.rollout) { envState(e, 'Waiting', st.mr.state === 'merged' ? '' : 'waits for the merge'); }
  if (st.mr.state === 'merged' && lib.SHA.test(sha)) { st.sha = sha; push({ k: 'mainPipelines', sha: sha }); }
};
const jobName = (env) => cfg.applyJobPattern.replace('{env}', env);
const collectJobs = (jobs) => {
  for (const e of cfg.rollout) {
    const j = jobs.filter((x) => x && x.name === jobName(e)).sort((a, b) => b.id - a.id)[0];
    if (j) { st.jobs[e] = { status: lib.str(j.status), id: String(j.id), webUrl: lib.webUrl(cfg, j.web_url) }; }
  }
};
const queueEnvChecks = () => {
  if (q.todo.some((x) => x.k === 'bridges' || x.k === 'jobs')) { return; }
  const terminal = ACTIVE_PIPE.indexOf(st.pipeline.status) < 0;
  for (const e of cfg.rollout) {
    const j = st.jobs[e];
    if (j) { envState(e, fromJob(j), j.status === 'manual' ? 'manual job: run it in GitLab' : (j.status === 'skipped' ? 'skipped' : '')); }
    else { envState(e, 'Waiting', terminal ? 'no job for this environment in the pipeline' : ''); }
    if (!j || ['manual', 'canceled', 'skipped', 'created'].indexOf(j.status) >= 0) {
      if (!j && !terminal) { continue; }
      push({ k: 'deployments', environment: cfg.environmentPattern.replace('{env}', e), env: e });
    }
  }
};
H['status:mr'] = () => { if (!ok) { gone(); return; } takeMr(o); };
H['status:mrsBySource'] = () => {
  if (!ok) { gone(); return; }
  const mr = list[0];
  if (mr) { takeMr(mr); return; }
  st.mr = null;
  for (const e of cfg.rollout) { envState(e, 'Waiting', 'branch only: no merge request yet'); }
};
H['status:mainPipelines'] = () => {
  if (!ok) { gone(); return; }
  const p = list[0];
  if (!p) { for (const e of cfg.rollout) { envState(e, 'Waiting', 'the pipeline has not started'); } st.pipeline = null; return; }
  st.pipeline = { id: String(p.id), status: lib.str(p.status), webUrl: lib.webUrl(cfg, p.web_url) };
  st.jobs = {};
  st.bridgesRead = false;
  push({ k: 'jobs', id: st.pipeline.id });
};
H['status:jobs'] = () => {
  if (!ok) { gone(); return; }
  collectJobs(list);
  if (cfg.childPipelines && !st.bridgesRead && cfg.rollout.some((e) => !st.jobs[e])) { st.bridgesRead = true; push({ k: 'bridges', id: st.pipeline.id }); }
  queueEnvChecks();
};
H['status:bridges'] = () => {
  if (ok) {
    for (const b of list.slice(0, 3)) {
      const d = b && b.downstream_pipeline;
      if (d && lib.NUM.test(String(d.id))) { push({ k: 'jobs', id: String(d.id) }); }
    }
  }
  queueEnvChecks();
};
H['status:deployments'] = () => {
  if (!ok) { return; }
  const env = item.env;
  const ours = list.filter((d) => d && d.sha === st.sha)[0];
  const latestOk = list.filter((d) => d && d.status === 'success')[0];
  if (ours && ours.status === 'blocked') { envState(env, 'Needs approval', 'waiting for approval'); push({ k: 'deployment', id: String(ours.id), env: env }); return; }
  if (ours && ours.status === 'success') { envState(env, 'Live', ''); return; }
  if (latestOk && latestOk.sha !== st.sha && lib.SHA.test(String(latestOk.sha)) && st.mr && Date.parse(latestOk.created_at) > Date.parse(st.mr.mergedAt)) {
    push({ k: 'mergeBase', a: st.sha, b: latestOk.sha, env: env });
  }
};
H['status:deployment'] = () => {
  if (!ok) { return; }
  const approved = lib.arr(o.approvals).filter((a) => a && a.status === 'approved').length;
  const pending = Number(o.pending_approval_count) || 0;
  envState(item.env, 'Needs approval', pending + approved > 0 ? approved + ' of ' + (approved + pending) + ' approvals' : 'waiting for approval');
};
H['status:mergeBase'] = () => {
  if (ok && o.id === st.sha) { envState(item.env, 'Live', 'via a newer commit ' + String(item.b).slice(0, 8)); push({ k: 'commitMrs', sha: item.b, env: item.env }); }
};
H['status:commitMrs'] = () => {
  const mr = list[0];
  if (ok && mr && mr.iid) { envState(item.env, 'Live', 'via !' + Number(mr.iid)); }
};
// ---- Requests list ------------------------------------------------------------------------
H['requests:mrList'] = () => {
  s.requests = { list: [], branches: [], at: now };
  if (!ok) { gone(); return; }
  s.requests.list = list.slice(0, 20).map((m) => Object.assign({ iid: String(m.iid), title: lib.redact(m.title, 200), state: lib.str(m.state),
    webUrl: lib.webUrl(cfg, m.web_url), updatedAt: lib.str(m.updated_at).slice(0, 16).replace('T', ' '), sourceBranch: lib.str(m.source_branch) },
  contract.footerOf(m.description)));
};
H['requests:requestBranches'] = () => {
  if (!ok) { gone(); return; }
  const withMr = s.requests.list.map((m) => m.sourceBranch);
  s.requests.branches = list.filter((b) => b && contract.isRequestBranch(b.name) && withMr.indexOf(b.name) < 0).slice(0, 20).map((b) => {
    const head = lib.obj(b.commit);
    const t = lib.str(head.title);
    const kind = /^pingone-promote: preview request /.test(t) ? 'preview' : (/^pingone-promote: submit request /.test(t) ? 'submit' : 'written');
    return { name: b.name, webUrl: lib.webUrl(cfg, b.web_url), sha: lib.SHA.test(lib.str(head.id)) ? head.id : '', kind: kind, cancellable: kind !== 'written',
      updatedAt: lib.str(head.committed_date).slice(0, 16).replace('T', ' ') };
  });
};

const h = H[q.phase + ':' + item.k];
if (!h) { throw new Error('no handler for ' + q.phase + ':' + item.k); }
h();
if (action === 'delete') { return lib.out(s, 'delete'); }
if (action === 'none') { action = 'render'; }
if (q.todo.length && q.calls < lib.MAX_CALLS) { return lib.out(s, lib.nextRead(s)); }
if (q.todo.length) { lib.note(s, 'warn', 'GitLab is slow to answer; some details were not checked this time.'); q.todo = []; }

// ---- end of the queue: settle the screen ---------------------------------------------------
if (q.phase === 'poll' && run && !run.done) {
  s.screen = 'working';
  const age = now - run.startedAt;
  run.noPipeline = !run.pipelineId && age > cfg.pipelineAppearSeconds * 1000;
  run.waitingRunner = run.jobStatus === 'pending' || run.jobStatus === 'created';
  // resource_group pingone-promote-requests: one request job at a time.
  run.waitingTurn = run.jobStatus === 'waiting_for_resource';
  if (now > run.deadline) {
    run.auto = false;
    run.timedOut = true;
    if (run.kind === 'submit') { s.sub = { state: 'unknown', key: run.key }; }
  } else { run.timedOut = false; }
}
if (q.phase === 'status' && st) {
  st.checkedAt = now;
  const p = st.pipeline;
  const open = st.mr && st.mr.state === 'opened';
  const needs = cfg.rollout.some((e) => st.envs[e] && (st.envs[e].state === 'Needs approval' || st.envs[e].state === 'Running'));
  const active = (st.mr && st.mr.state === 'merged' && (!p || ACTIVE_PIPE.indexOf(p.status) >= 0)) || open || !st.mr || needs;
  st.auto = active && !st.paused;
  if (st.auto && now - st.startedAt > cfg.statusMaxMinutes * 60000) {
    st.auto = false;
    st.expired = true;
  } else { st.expired = false; }
  st.final = !active;
}
s.screen = s.screen || 'search';
return lib.out(s, 'render');
