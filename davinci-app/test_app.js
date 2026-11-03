'use strict';
// Node tests for the PingOne Promote app flow (no tenant needed):
//   node test_app.js [EXPORT.json]
// 1. Every function in the export is byte-identical to fn/*.js as gen_app.py wraps it.
// 2. Unit tests for fn/_lib.js and fn/_contract.js.
// 3. The exported graph itself runs against a fake PingOne and a fake GitLab:
//    evaluators, saves, screens and functions, with a simulated person
//    clicking through screens (as the App screen's script would post them).
// Test IDs are built at run time so no ID-shaped literal lives in the repo.
const assert = require('assert');
const fs = require('fs');
const path = require('path');

const HERE = __dirname;
const EXPORT = process.argv[2] || path.join(HERE, 'flow', 'pingone-promote-app.flow.json');
const flow = JSON.parse(fs.readFileSync(EXPORT, 'utf8')).flows[0];
const NODES = {};
for (const n of flow.graphData.elements.nodes) { NODES[n.data.id] = n.data; }
const SUCC = {};
const PRED = {};
for (const e of flow.graphData.elements.edges) {
  (SUCC[e.data.source] = SUCC[e.data.source] || []).push(e.data.target);
  (PRED[e.data.target] = PRED[e.data.target] || []).push(e.data.source);
}
const title = (id) => (NODES[id].properties && NODES[id].properties.nodeTitle ? NODES[id].properties.nodeTitle.value : id);
const byTitle = (t) => Object.keys(NODES).filter((x) => title(x) === t)[0];

let passed = 0;
const ok = (cond, msg) => { assert.ok(cond, msg); passed += 1; };
const eq = (a, b, msg) => { assert.deepStrictEqual(a, b, msg); passed += 1; };

// ---- 1. shipped code == sources ----------------------------------------------------
const read = (f) => fs.readFileSync(path.join(HERE, 'fn', f), 'utf8').replace(/\r\n/g, '\n');
const strip = (js) => js.split('\n').map((l) => l.trim()).filter((l) => l && !l.startsWith('//')).join('\n');
const CONTRACT = strip(read('_contract.js'));
const LIB = strip(read('_lib.js'));
const wrap = (step) => 'module.exports = a = async ({params}) => {\nconst contract = (() => {\n' + CONTRACT + '\nreturn contract;\n})();\nconst lib = (() => {\n' + LIB +
  '\nreturn lib;\n})();\n// ---- step: ' + step + ' ----\n' + read(step + '.js') + '\n};\n';
const compile = (code) => {
  const m = { exports: null };
  // eslint-disable-next-line no-new-func
  new Function('module', code.replace('module.exports = a = ', 'module.exports = '))(m);
  return m.exports;
};
const FNS = {};
for (const id of Object.keys(NODES)) {
  const d = NODES[id];
  if (d.capabilityName !== 'customFunction') { continue; }
  const code = d.properties.code.value;
  const step = code.split('// ---- step: ')[1].split(' ----')[0];
  eq(code, wrap(step), 'shipped code of "' + title(id) + '" matches fn/' + step + '.js');
  FNS[id] = compile(code);
}
for (const s of fs.readdirSync(path.join(HERE, 'fn')).filter((f) => !f.startsWith('_') && f.endsWith('.js')).map((f) => f.slice(0, -3))) {
  ok(Object.keys(FNS).some((id) => NODES[id].properties.code.value.indexOf('// ---- step: ' + s + ' ----') >= 0), 'fn/' + s + '.js is used in the flow');
}
// eslint-disable-next-line no-new-func
const both = new Function('const contract = (() => {\n' + CONTRACT + '\nreturn contract;\n})();\nconst lib = (() => {\n' + LIB + '\nreturn lib;\n})();\nreturn { lib: lib, contract: contract };')();
const lib = both.lib;
const contract = both.contract;

// ---- test IDs and secrets (built, never written out) ---------------------------------------
const hex = (n, len) => (n.toString(16) + '0'.repeat(len)).slice(0, len);
const uuid = (n) => hex(n + 0xa0, 8) + '-0000-4000-8000-' + hex(n + 0xb0, 12);
const dvid = (n) => 'd' + hex(n + 0xc0, 31);
const SRC = uuid(1);
const QAENV = uuid(3);
const WORKER = uuid(2);
const PROJECT = '4242';
const BASIC = Buffer.from('client:secret-value-for-tests').toString('base64');
const GLTOKEN = 'glpat-' + 'T'.repeat(20);
const ACCESS = 'access-' + 'Z'.repeat(40);
const SECRETS = [BASIC, GLTOKEN, ACCESS, 'secret-value-for-tests'];
const GLWEB = 'https://gitlab.example.com/g/config';
const GLAPI = 'https://gitlab.example.com/api/v4';
const BASE = GLAPI + '/projects/' + PROJECT;

// ---- 2. unit tests ---------------------------------------------------------------------------
for (const t of ['', 'a', 'ab', 'abc', 'é€😀 {{x}}', JSON.stringify({ a: [1, 'ü'] }), 'x'.repeat(1001)]) {
  eq(lib.b64(t), Buffer.from(t, 'utf8').toString('base64'), 'base64 matches Node for ' + JSON.stringify(t).slice(0, 20));
  eq(lib.unb64(Buffer.from(t, 'utf8').toString('base64')), t, 'base64 decodes ' + JSON.stringify(t).slice(0, 20));
}
eq(lib.esc('<a href="x">{{global.company.variables.ppGitlabToken}}</a>'), '&lt;a href=&quot;x&quot;&gt;&#123;&#123;global.company.variables.ppGitlabToken&#125;&#125;&lt;/a&gt;', 'esc escapes HTML and braces');
ok(lib.redact('Bearer ' + ACCESS + ' ' + GLTOKEN + ' env ' + SRC).indexOf(ACCESS) < 0 && lib.redact(GLTOKEN).indexOf(GLTOKEN) < 0 && lib.redact(SRC).indexOf(SRC) < 0, 'redact removes tokens and IDs');
ok(lib.looksSecret(GLTOKEN) && lib.looksSecret('Bearer ' + ACCESS) && !lib.looksSecret('https://login.example.com/qa'), 'secret-looking values are spotted');
const rid = contract.newRequestId(Date.UTC(2026, 10, 3, 14, 5, 1), 'k3j9q2');
eq(rid, '20261103-140501-k3j9q2', 'request id format');
ok(contract.isRequestBranch('pingone-promote/' + rid) && !contract.isRequestBranch('main') && !contract.isRequestBranch('pingone-promote/x') && !contract.isRequestBranch('pingone-promote/' + rid + '/../main'), 'request branch check');
const cfg0 = lib.checkSettings({ apiOrigin: 'https://api.pingone.com', authOrigin: 'https://auth.pingone.com', workerEnvironmentId: WORKER, sourceEnvs: 'dev=' + SRC,
  pathFlows: '/v1/environments/{envId}/flows', rolloutEnvs: 'qa,stage,prod', gitlabApi: GLAPI, gitlabProjectId: PROJECT, allowedGroups: '*' }).cfg;
assert.throws(() => lib.gl(cfg0, 'deleteBranch', { branch: 'main' })); passed += 1;
assert.throws(() => lib.gl(cfg0, 'jobs', { id: '1/../../x' })); passed += 1;
assert.throws(() => lib.gl(cfg0, 'pipelines/1/play')); passed += 1;
assert.throws(() => lib.queue({ cfg: cfg0 }, 'x', [{ k: 'commit' }]), /only reads/); passed += 1;
eq(lib.webUrl(cfg0, 'https://evil.example/x'), '', 'links to other hosts are dropped');
eq(lib.webUrl(cfg0, 'javascript:alert(1)'), '', 'script links are dropped');
const p1 = lib.checkSettings({});
ok(p1.problems.length >= 6 && JSON.stringify(p1).indexOf('not set') > 0, 'missing settings are listed');
const flowsFake = [{ id: 'a1', name: 'Login' }, { id: 'a2', name: 'Login - MFA' }, { id: 'a3', name: 'Registration' }];
eq(lib.searchFlows(flowsFake, 'LOGIN').matches.map((f) => f.id), ['a1', 'a2'], 'search is a case-insensitive substring; ambiguous lists all');
eq(lib.searchFlows(flowsFake, 'a3').matches.map((f) => f.id), ['a3'], 'search by exact ID');
eq(lib.searchFlows(flowsFake, 'zzz').total, 0, 'no match');
const req0 = contract.buildRequest({ requestId: rid, requestedBy: 'pat', source: 'dev', flow: { id: 'a1', name: 'Login' },
  options: { mode: 'mr', title: 't', description: 'd', commitMessage: 'c', deps: {}, env: {}, secrets: {} }, dryRun: true });
const REQUEST_KEYS = ['requestVersion', 'requestId', 'requestedBy', 'source', 'flows', 'dependencyChoices', 'envValues', 'secretMappings', 'mode', 'branchName', 'commitMessage', 'title', 'description', 'dryRun'];
eq(Object.keys(req0), REQUEST_KEYS, 'request.json has exactly the contract fields, in order');
const cb = contract.commitBody(req0, 'main', '');
ok(cb.start_branch === 'main' && cb.actions[0].action === 'create' && !cb.actions[0].last_commit_id && cb.branch === 'pingone-promote/' + rid, 'first commit creates the branch');
const cb2 = contract.commitBody(req0, 'main', 'f'.repeat(40));
ok(!cb2.start_branch && cb2.actions[0].action === 'update' && cb2.actions[0].last_commit_id === 'f'.repeat(40), 'later commits update with last_commit_id');
eq(JSON.parse(Buffer.from(cb.actions[0].content, 'base64').toString('utf8')), req0, 'the commit carries request.json as base64');
const nr = contract.normalizeResult({ status: 'ok', changes: [{ address: 'a.b', action: 'weird' }, {}], blockers: { qa: ['x', { var: 'v', message: 'm' }], prod: [] }, requiredSecrets: ['k'], error: { code: 'nope', message: 'm ' + GLTOKEN } });
ok(nr.problems.length === 2 && nr.blockers.qa.length === 2 && !nr.blockers.prod && nr.requiredSecrets[0].key === 'k' && nr.error.code === 'unknown' && nr.error.message.indexOf(GLTOKEN) < 0, 'result normalisation keeps problems visible and redacts');
eq(contract.footerOf('x\nRequested-by: pat\nRequest-id: ' + rid), { requestedBy: 'pat', requestId: rid }, 'MR footer parsed');
eq(contract.mrIid(GLWEB + '/-/merge_requests/17'), '17', 'MR iid from URL');

// ---- 3. fake PingOne + fake GitLab --------------------------------------------------------------
let CLOCK = Date.UTC(2026, 10, 3, 9, 0, 0);
const realNow = Date.now;
Date.now = () => CLOCK;
let RANDOM = 0;
Math.random = () => { RANDOM = (RANDOM + 0.137) % 1; return RANDOM; };
let SHA_N = 0;
const newSha = () => { SHA_N += 1; return ('c' + SHA_N.toString(16)).padEnd(40, '0'); };

function makeFlows() {
  return [
    { id: dvid(1), name: 'Login' }, { id: dvid(2), name: 'Login - MFA' }, { id: dvid(3), name: 'Registration' }, { id: dvid(4), name: 'Password reset' },
    { id: dvid(5), name: '<img src=x onerror=alert(1)> evil' }, { id: dvid(6), name: '{{global.company.variables.ppGitlabToken}}' }, { id: dvid(7), name: 'Account recovery' }
  ];
}
const PREVIEW = (req) => {
  const env = req.envValues || {};
  const blockers = {};
  for (const e of ['qa', 'stage', 'prod']) {
    if (!(env[e] && env[e].base_url)) { blockers[e] = [{ code: 'missing_env_value', var: 'base_url', message: 'base_url has no value for ' + e }]; }
  }
  return { status: 'ok', diffUrl: GLWEB + '/-/compare/main...' + req.branchName,
    changes: [{ address: 'pingone_davinci_flow.login', action: 'updated', role: 'selected' }, { address: 'pingone_davinci_flow.mfa_sub', action: 'added', role: 'dependency' },
      { address: 'pingone_davinci_connector_instance.http', action: 'kept_differs', role: 'dependency' }, { address: 'pingone_davinci_variable.theme', action: 'unchanged' }],
    requiredSecrets: [{ key: 'davinci_variable_PingFedAuthNCreds_value', varName: 'davinci_variable_PingFedAuthNCreds_value', field: 'pingfed_authn_creds' }],
    blockers: blockers };
};

function world(opt) {
  const o = Object.assign({ pageSize: 3, tokenStatus: 200, pipelineDelay: 0, submitPipelineDelay: 0, jobStatuses: ['pending', 'running', 'success'], submitError: null,
    commitFail: null, noBlockers: false, artifactAsText: false, identity: { userId: 'u-1', username: 'pat.operator', name: 'Pat Operator', groups: ['Promote Operators'] } }, opt || {});
  const flows = makeFlows();
  const calls = [];
  const g = { branches: { main: { commits: [newSha()] } }, files: {}, pipelines: [], mrs: [], deployments: [], commits: [], nextPipe: 500, nextJob: 9000, nextMr: 40, deletes: [] };
  const head = (b) => g.branches[b].commits[g.branches[b].commits.length - 1];
  const branchObj = (b) => ({ name: b, web_url: GLWEB + '/-/tree/' + b, commit: { id: head(b), title: g.branches[b].titles[g.branches[b].titles.length - 1], committed_date: '2026-11-03T09:00:00Z' } });
  g.branches.main.titles = ['init'];
  const commitTo = (b, t) => { const s = newSha(); g.branches[b].commits.push(s); g.branches[b].titles.push(t); return s; };
  const page = (items, pg, base) => {
    const start = pg * o.pageSize;
    const body = { _embedded: { flows: items.slice(start, start + o.pageSize) }, count: items.length };
    if (start + o.pageSize < items.length) { body._links = { next: { href: base + '?attributes=id,name&cursor=' + (pg + 1) } }; }
    return { status: 200, body: body };
  };
  const runJob = (p) => {
    // The request job finishes: decide the result and do what the backend would do.
    const req = p.req;
    let res;
    if (req.dryRun) {
      res = PREVIEW(req);
      if (o.noBlockers) { res.blockers = {}; res.requiredSecrets = []; }
      if (o.previewError) { res.error = o.previewError; }
    } else if (o.submitError) {
      const code = o.submitError.shift ? o.submitError.shift() : o.submitError;
      if (o.submitError.length === 0) { o.submitError = null; }
      res = { status: 'error', changes: [], error: { code: code, message: code + ' reported by the request job for ' + req.branchName } };
      if (code === 'partial') { commitTo(req.branchName, 'pingone-promote: generated change'); res.branchUrl = GLWEB + '/-/tree/' + req.branchName; }
      if (code === 'blocked') { res.blockers = { prod: [{ code: 'missing_env_value', var: 'base_url', message: 'base_url has no value for prod' }] }; }
    } else {
      commitTo(req.branchName, 'pingone-promote: generated change for ' + req.flows[0].name);
      delete g.files[req.branchName];
      res = { status: 'ok', changes: PREVIEW(req).changes };
      if (req.mode === 'mr') {
        g.nextMr += 1;
        g.mrs.push({ iid: g.nextMr, title: req.title, state: 'opened', web_url: GLWEB + '/-/merge_requests/' + g.nextMr, source_branch: req.branchName,
          description: req.description + '\n\nRequested-by: ' + req.requestedBy + '\nRequest-id: ' + req.requestId + '\n', labels: ['pingone-promote'], updated_at: '2026-11-03T09:10:00Z' });
        res.mrUrl = GLWEB + '/-/merge_requests/' + g.nextMr;
      } else { res.branchUrl = GLWEB + '/-/tree/' + req.branchName; }
    }
    p.result = res;
  };
  const handler = (method, url, headers, body) => {
    calls.push({ method: method, url: url, headers: headers, body: body });
    if (method === 'POST' && url === 'https://auth.pingone.com/' + WORKER + '/as/token') {
      assert.strictEqual(headers.Authorization, 'Basic ' + BASIC);
      assert.strictEqual(body, 'grant_type=client_credentials');
      return o.tokenStatus === 200 ? { status: 200, body: { access_token: ACCESS, token_type: 'Bearer', expires_in: 3600 } }
        : { status: o.tokenStatus, body: { code: 'invalid_client', message: 'client ' + WORKER + ' Bearer ' + ACCESS + ' rejected' } };
    }
    if (url.indexOf('https://api.pingone.com/') === 0) {
      assert.strictEqual(method, 'GET', 'PingOne is read-only');
      assert.strictEqual(headers.Authorization, 'Bearer ' + ACCESS);
      const m = /^https:\/\/api\.pingone\.com\/v1\/environments\/([^/]+)\/flows\?attributes=id,name(?:&cursor=([0-9]+))?$/.exec(url);
      assert.ok(m, 'only the flow list is read: ' + url);
      return page(m[1] === SRC ? flows : [], Number(m[2] || 0), 'https://api.pingone.com/v1/environments/' + m[1] + '/flows');
    }
    assert.ok(url.indexOf(BASE + '/') === 0, 'GitLab call to our project only: ' + url);
    assert.strictEqual(headers['PRIVATE-TOKEN'], GLTOKEN);
    assert.ok(!headers.Authorization, 'no other credential to GitLab');
    const rest = url.slice(BASE.length);
    const bare = rest.split('?')[0];
    const qs = {};
    for (const kv of (rest.split('?')[1] || '').split('&').filter((x) => x)) { const i = kv.indexOf('='); qs[decodeURIComponent(kv.slice(0, i))] = decodeURIComponent(kv.slice(i + 1)); }
    const rb = (enc) => decodeURIComponent(enc);
    if (method === 'POST') {
      assert.strictEqual(bare, '/repository/commits', 'the only GitLab write is the request-file commit');
      const b = JSON.parse(body);
      g.commits.push(b);
      assert.ok(contract.isRequestBranch(b.branch), 'commits go to a request branch only');
      assert.strictEqual(b.actions.length, 1);
      const a = b.actions[0];
      assert.strictEqual(a.file_path, '.pingone-promote/request.json');
      assert.strictEqual(a.encoding, 'base64');
      const req = JSON.parse(Buffer.from(a.content, 'base64').toString('utf8'));
      const fail = o.commitFail && o.commitFail.length ? o.commitFail.shift() : null;
      if (fail && !fail.write) { return { status: fail.status, body: { message: fail.message || 'upstream error' } }; }
      if (a.action === 'create') {
        assert.strictEqual(b.start_branch, 'main');
        if (g.branches[b.branch]) { return { status: 400, body: { message: 'A branch called ' + b.branch + ' already exists' } }; }
        g.branches[b.branch] = { commits: [head('main')], titles: ['init'] };
      } else {
        assert.strictEqual(a.action, 'update');
        if (!g.branches[b.branch]) { return { status: 400, body: { message: 'You can only create or edit files when you are on a branch' } }; }
        const f = g.files[b.branch];
        if (!f || f.last !== a.last_commit_id) { return { status: 400, body: { message: 'You are attempting to update a file that has changed since you started editing it.' } }; }
      }
      const sha = commitTo(b.branch, b.commit_message.split('\n')[0]);
      g.files[b.branch] = { content: a.content, last: sha };
      g.nextPipe += 1;
      g.nextJob += 1;
      g.pipelines.push({ id: g.nextPipe, ref: b.branch, sha: sha, status: 'pending', seen: 0, req: req, job: { id: g.nextJob, i: 0, statuses: o.jobStatuses.slice() } });
      if (fail) { return { status: fail.status, body: { message: 'Bad gateway' } }; }
      return { status: 201, body: { id: sha, short_id: sha.slice(0, 8), title: b.commit_message.split('\n')[0], web_url: GLWEB + '/-/commit/' + sha } };
    }
    if (method === 'DELETE') {
      const m = /^\/repository\/branches\/(.+)$/.exec(bare);
      assert.ok(m && contract.isRequestBranch(rb(m[1])), 'only request branches are deleted');
      g.deletes.push(rb(m[1]));
      delete g.branches[rb(m[1])];
      delete g.files[rb(m[1])];
      return { status: 204, body: '' };
    }
    assert.strictEqual(method, 'GET');
    let m;
    if ((m = /^\/repository\/files\/([^/]+)$/.exec(bare))) {
      assert.strictEqual(rb(m[1]), '.pingone-promote/request.json');
      const f = g.files[qs.ref];
      return f ? { status: 200, body: { file_path: '.pingone-promote/request.json', content: f.content, encoding: 'base64', last_commit_id: f.last } } : { status: 404, body: { message: '404 File Not Found' } };
    }
    if ((m = /^\/repository\/branches\/(.+)$/.exec(bare))) { const b = rb(m[1]); return g.branches[b] ? { status: 200, body: branchObj(b) } : { status: 404, body: { message: '404 Branch Not Found' } }; }
    if (bare === '/repository/branches') {
      assert.strictEqual(qs.search, '^pingone-promote/');
      return { status: 200, body: Object.keys(g.branches).filter((b) => b.indexOf('pingone-promote/') === 0).map(branchObj) };
    }
    if (bare === '/pipelines') {
      if (qs.ref === 'main') { return { status: 200, body: g.pipelines.filter((p) => p.ref === 'main' && p.sha === qs.sha).map((p) => ({ id: p.id, status: p.status, web_url: GLWEB + '/-/pipelines/' + p.id })) }; }
      assert.ok(contract.isRequestBranch(qs.ref) && /^[0-9a-f]{40}$/.test(qs.sha), 'branch pipelines by ref and sha');
      const ps = g.pipelines.filter((p) => p.ref === qs.ref && p.sha === qs.sha);
      for (const p of ps) { p.seen += 1; }
      return { status: 200, body: ps.filter((p) => p.seen > (p.req.dryRun ? o.pipelineDelay : o.submitPipelineDelay)).map((p) => ({ id: p.id, status: p.status, ref: p.ref, sha: p.sha, web_url: GLWEB + '/-/pipelines/' + p.id })) };
    }
    if ((m = /^\/pipelines\/([0-9]+)\/jobs$/.exec(bare))) {
      const p = g.pipelines.filter((x) => x.id === Number(m[1]))[0];
      if (p.ref === 'main' || p.child) { return { status: 200, body: p.jobs.map((j) => ({ id: j.id, name: j.name, status: j.status, web_url: GLWEB + '/-/jobs/' + j.id, pipeline: { id: p.id, status: p.status } })) }; }
      const j = p.job;
      const st = j.statuses[Math.min(j.i, j.statuses.length - 1)];
      j.i += 1;
      if (['success', 'failed', 'canceled'].indexOf(st) >= 0 && !p.result) { runJob(p); p.status = st; }
      return { status: 200, body: [{ id: 1, name: 'lint', status: 'success' }, { id: j.id, name: 'pingone-promote:request', status: st, started_at: '2026-11-03T09:00:05Z', web_url: GLWEB + '/-/jobs/' + j.id, pipeline: { id: p.id, status: p.status } }] };
    }
    if ((m = /^\/pipelines\/([0-9]+)\/bridges$/.exec(bare))) {
      const p = g.pipelines.filter((x) => x.id === Number(m[1]))[0];
      return { status: 200, body: (p.bridges || []).map((id) => ({ id: 1, name: 'child', downstream_pipeline: { id: id } })) };
    }
    if ((m = /^\/jobs\/([0-9]+)\/artifacts\/result\.json$/.exec(bare))) {
      const p = g.pipelines.filter((x) => x.job && x.job.id === Number(m[1]))[0];
      if (!p || !p.result || o.noArtifact) { return { status: 404, body: { message: '404 Not Found' } }; }
      return { status: 200, body: o.artifactAsText ? JSON.stringify(p.result) : JSON.parse(JSON.stringify(p.result)) };
    }
    if ((m = /^\/merge_requests\/([0-9]+)$/.exec(bare))) { const mr = g.mrs.filter((x) => x.iid === Number(m[1]))[0]; return mr ? { status: 200, body: mr } : { status: 404, body: {} }; }
    if (bare === '/merge_requests') {
      if (qs.source_branch) { return { status: 200, body: g.mrs.filter((x) => x.source_branch === qs.source_branch) }; }
      assert.strictEqual(qs.labels, 'pingone-promote');
      return { status: 200, body: g.mrs.slice().reverse() };
    }
    if (bare === '/deployments') { return { status: 200, body: g.deployments.filter((d) => d.environment.name === qs.environment).sort((a, b) => b.id - a.id) }; }
    if ((m = /^\/deployments\/([0-9]+)$/.exec(bare))) { return { status: 200, body: g.deployments.filter((d) => d.id === Number(m[1]))[0] }; }
    if (bare === '/repository/merge_base') { return { status: 200, body: { id: g.mergeBase || '' } }; }
    if ((m = /^\/repository\/commits\/([0-9a-f]{40})\/merge_requests$/.exec(bare))) { return { status: 200, body: [{ iid: 77 }] }; }
    throw new Error('unexpected call ' + method + ' ' + url);
  };
  return { o: o, handler: handler, calls: calls, g: g, flows: flows };
}

const SETTINGS = { ppWorkerEnvironmentId: WORKER, ppSourceEnvs: 'dev=' + SRC + ', qa=' + QAENV, ppGitlabApi: GLAPI, ppGitlabProjectId: PROJECT, ppAllowedGroups: 'Promote Operators' };
const unesc = (s) => s.replace(/&quot;/g, '"').replace(/&#39;/g, "'").replace(/&lt;/g, '<').replace(/&gt;/g, '>').replace(/&#123;/g, '{').replace(/&#125;/g, '}').replace(/&amp;/g, '&');
// What the App screen's script would post: every data-pp control's value, then the test's edits.
function formOf(html, edits) {
  const data = {};
  const set = (p, v) => { let o = data; for (let k = 0; k < p.length - 1; k++) { o[p[k]] = o[p[k]] || {}; o = o[p[k]]; } o[p[p.length - 1]] = v; };
  const re = /<(input|textarea|select)\b([^>]*)>([\s\S]*?(?=<\/textarea>|<\/select>))?/g;
  let m;
  while ((m = re.exec(html))) {
    const a = m[2];
    const dp = /data-pp="([^"]*)"/.exec(a);
    if (!dp) { continue; }
    const p = JSON.parse(unesc(dp[1]));
    const type = (/type="([a-z]+)"/.exec(a) || [0, m[1]])[1];
    const val = unesc((/value="([^"]*)"/.exec(a) || [0, ''])[1]);
    if (type === 'radio') { if (/\bchecked\b/.test(a)) { set(p, val); } continue; }
    if (type === 'checkbox') { set(p, /\bchecked\b/.test(a)); continue; }
    if (m[1] === 'textarea') { set(p, unesc(m[3] || '')); continue; }
    if (m[1] === 'select') { const sel = /<option value="([^"]*)" selected>/.exec(m[3] || '') || /<option value="([^"]*)"/.exec(m[3] || ''); set(p, sel ? unesc(sel[1]) : ''); continue; }
    set(p, val);
  }
  const merge = (dst, src) => { for (const k of Object.keys(src || {})) { if (src[k] && typeof src[k] === 'object') { dst[k] = merge(dst[k] && typeof dst[k] === 'object' ? dst[k] : {}, src[k]); } else { dst[k] = src[k]; } } return dst; };
  return merge(data, edits);
}
const buttonOf = (html, value) => {
  const m = new RegExp('<button[^>]*data-skbuttonvalue="' + value.replace(/[.*+?^${}()|[\]\\:]/g, '\\$&') + '"([^>]*)>').exec(html);
  return m ? { disabled: /\bdisabled\b/.test(m[1]), onEdit: /data-pp-enable-on-edit/.test(m[1]) } : null;
};
const autoOf = (html) => { const m = /<div id="pp-auto" data-value="([A-Z_]+)" data-delay="([0-9]+)"/.exec(html); return m ? { value: m[1], delay: Number(m[2]) } : null; };
const visible = (html) => unesc(html.replace(/<[^>]+>/g, ' ')).replace(/\s+/g, ' ');

// Run the exported graph. `steps` is the person: each step answers one App screen.
//   { click, form, expect, force }  click a button (it must be there and enabled unless force)
//   { auto: true, until: RegExp }   let the screen's automatic check fire until the screen matches
//   { do: fn }                      change the fake world, then continue with the next step
async function run(opt, steps) {
  const o = Object.assign({ strings: false, settings: {} }, opt);
  const w = o.world || world(o.worldOpt);
  const flowVars = {};
  for (const v of flow.variables) { if (v.context === 'flow') { flowVars[v.name.split('##SK##')[0]] = v.fields.value; } }
  Object.assign(flowVars, SETTINGS, o.settings);
  const company = { ppWorkerBasicAuth: BASIC, ppGitlabToken: GLTOKEN };
  const inst = {};
  const outputs = {};
  const pages = [];
  const budget = [];
  const resolve = (ref) => {
    const p = ref.split('.');
    if (p[0] === 'local') {
      assert.ok(outputs[p[1]] !== undefined, 'reads output of a node that has not run: ' + title(p[1]));
      let v = outputs[p[1]];
      for (const k of p.slice(4)) { v = v === undefined || v === null ? undefined : v[k]; }
      return v;
    }
    if (p[1] === 'flow') { return flowVars[p[3]]; }
    if (p[1] === 'company') { return company[p[3]]; }
    if (p[1] === 'variables') { assert.ok(p[2] in inst, 'reads unset flow-instance variable ' + p[2]); return inst[p[2]]; }
    throw new Error('unknown reference ' + ref);
  };
  const text = (v) => (v === undefined || v === null ? '' : typeof v === 'object' ? JSON.stringify(v) : String(v));
  const slate = (raw, keepType) => {
    const kids = JSON.parse(raw)[0].children.filter((c) => c.type === 'link' || c.text !== '');
    if (keepType && kids.length === 1 && kids[0].type === 'link') { return resolve(kids[0].data.slice(2, -2)); }
    return kids.map((c) => (c.type === 'link' ? text(resolve(c.data.slice(2, -2))) : c.text)).join('');
  };
  const template = (s) => s.replace(/\{\{([^}]+)\}\}/g, (m, r) => text(resolve(r)));
  let id = Object.keys(NODES).filter((x) => !PRED[x])[0];
  let si = 0;
  let segCalls = 0;
  let segButton = 'START';
  for (let stepN = 0; stepN < 100000; stepN++) {
    const d = NODES[id];
    const p = d.properties || {};
    let result = true;
    switch (d.capabilityName) {
      case 'startUiSubFlow': outputs[id] = Object.assign({}, w.o.identity); break;
      case 'customFunction': {
        const params = {};
        for (const v of p.variableInputList.value) {
          const val = slate(v.value, true);
          params[v.name] = o.strings && val && typeof val === 'object' ? JSON.stringify(val) : val;
        }
        outputs[id] = await FNS[id]({ params: params });
        for (const k of Object.keys(outputs[id])) { assert.ok(k in JSON.parse(p.outputSchema.value).properties, title(id) + ' returns undeclared ' + k); }
        break;
      }
      case 'AEqualsB': result = String(slate(p.leftValueA.value)) === String(slate(p.rightValueB.value)); outputs[id] = {}; break;
      case 'saveValue':
        for (const item of p.saveVariables.value) {
          const v = slate(item.value, true);
          inst[item.name] = item.type === 'object' && typeof v === 'string' ? JSON.parse(v || 'null') : v;
        }
        outputs[id] = {};
        break;
      case 'makeRestApiCall': {
        const headers = {};
        for (const h of p.headers.value) { headers[h.key] = slate(h.value); }
        const res = w.handler(p.httpMethod.value, slate(p.url.value), headers, p.raw ? template(p.raw.value) : undefined);
        segCalls += 1;
        outputs[id] = { rawResponse: { statusCode: res.status, body: res.body } };
        result = res.status >= 200 && res.status <= 299;
        break;
      }
      case 'customHTMLTemplate': {
        const html = template(p.customHTML.value);
        ok(html.indexOf('{{') < 0, 'no {{reference}} survives on a screen (' + title(id) + ')');
        budget.push({ button: segButton, calls: segCalls });
        segCalls = 0;
        pages.push({ title: title(id), html: html, state: JSON.parse(JSON.stringify(inst.ppState || {})) });
        if (!SUCC[id]) { return { w: w, pages: pages, inst: inst, budget: budget, last: html }; }
        let step;
        for (;;) {
          step = steps[si];
          if (!step) { return { w: w, pages: pages, inst: inst, budget: budget, last: html, open: true }; }
          if (step.do) { step.do(w, html); si += 1; continue; }
          if (step.auto && step.until.test(visible(html))) { si += 1; continue; }
          break;
        }
        if (step.expect) { ok(step.expect.test(visible(html)), 'screen matches ' + step.expect + ': ' + visible(html).slice(0, 400)); }
        let click;
        if (step.auto) {
          const a = autoOf(html);
          assert.ok(a, 'expected an automatic check (until ' + step.until + ') on: ' + visible(html).slice(0, 600));
          CLOCK += a.delay;
          click = a.value;
          if (++step.n > 300) { throw new Error('auto never reached ' + step.until); }
        } else {
          click = step.click;
          si += 1;
          CLOCK += 2000;
        }
        const b = buttonOf(html, click);
        assert.ok(b, 'button ' + click + ' is on the screen: ' + visible(html).slice(0, 500));
        // A disabled Submit that an edit turns back on (#57) may be clicked when the step edits the form.
        if (!step.force) { assert.ok(!b.disabled || (b.onEdit && step.form), 'button ' + click + ' is enabled: ' + visible(html).slice(0, 600)); }
        outputs[id] = { buttonValue: click, ppForm: JSON.stringify(formOf(html, step.form)) };
        segButton = click;
        break;
      }
      default: throw new Error('no simulation for ' + d.capabilityName);
    }
    const evs = SUCC[id] || [];
    assert.strictEqual(evs.length, 1, title(id) + ' must lead to one evaluator');
    const ev = NODES[evs[0]];
    const falses = Object.keys(ev.properties || {}).filter((k) => ev.properties[k].value === 'allTriggersFalse');
    const next = (SUCC[ev.id] || []).filter((t) => (result ? falses.indexOf(t) < 0 : falses.indexOf(t) >= 0));
    assert.strictEqual(next.length, 1, 'one way on from ' + title(id) + ' (result ' + result + ')');
    id = next[0];
  }
  throw new Error('flow did not end');
}
const auto = (until) => ({ auto: true, until: until, n: 0 });

// ---- checks every run gets ---------------------------------------------------------------------
const BANNED = /\b(target|targets|deploy\w*|promotion|destination|digest|fingerprint)\b/i;
const ALL_RUNS = [];
function common(r, label) {
  ALL_RUNS.push(r);
  for (const pg of r.pages) {
    const v = visible(pg.html);
    for (const s of SECRETS) { ok(pg.html.indexOf(s) < 0, label + ': no secret or token on screen "' + pg.title + '"'); }
    ok(pg.html.indexOf(SRC) < 0 && pg.html.indexOf(WORKER) < 0 && pg.html.indexOf(QAENV) < 0, label + ': no environment ID on screen');
    ok(!BANNED.test(v), label + ': vocabulary on "' + pg.title + '": ' + (BANNED.exec(v) || [''])[0]);
    ok(!/\bplan\b/i.test(v.replace(/pipeline plan job/gi, '')), label + ': "plan" only as "pipeline plan job"');
    ok(!/<script|onerror=/i.test(pg.html.replace(/&lt;img src=x onerror=alert\(1\)&gt;/g, '')), label + ': no markup injection on screen');
  }
  const st = JSON.stringify(r.inst.ppState || {}) + JSON.stringify(r.inst.ppFlows || {}) + String(r.inst.ppBody || '') + String(r.inst.ppUrl || '');
  for (const s of SECRETS) { ok(st.indexOf(s) < 0, label + ': no secret or token in flow-instance state'); }
  for (const c of r.w.calls) {
    if (c.url.indexOf('gitlab') > 0) { ok(c.headers.Authorization === undefined && JSON.stringify(c.body || '').indexOf(ACCESS) < 0 && JSON.stringify(c.body || '').indexOf(BASIC) < 0, label + ': PingOne credentials never go to GitLab'); }
    else { ok(JSON.stringify(c).indexOf(GLTOKEN) < 0, label + ': the GitLab token never goes to PingOne'); }
    if (c.method === 'POST' && c.url.indexOf('gitlab') > 0) {
      const req = Buffer.from(JSON.parse(c.body).actions[0].content, 'base64').toString('utf8');
      for (const s of SECRETS) { ok(req.indexOf(s) < 0 && c.body.indexOf(s) < 0, label + ': no secret in request.json or the commit'); }
      eq(Object.keys(JSON.parse(req)), REQUEST_KEYS, label + ': request.json has exactly the contract fields');
    }
  }
  const LIMITS = { START: 0, SEARCH: 2 + 3, POLL: 3, KEEP: 3, PREVIEW: 1, SUBMIT: 1, CHECK: 4, CANCEL: 2, CANCELB: 2, REFRESH: lib.MAX_CALLS, RESUME: lib.MAX_CALLS, WATCH: lib.MAX_CALLS, NAV_REQUESTS: 2 };
  for (const b of r.budget) {
    const key = b.button.split(':')[0].replace(/^OPENB?$/, 'WATCH');
    const lim = lib.has(LIMITS, key) ? LIMITS[key] : 0;
    ok(b.calls <= lim, label + ': ' + b.button + ' made ' + b.calls + ' REST calls (budget ' + lim + ')');
  }
}
const decodeReq = (c) => JSON.parse(Buffer.from(JSON.parse(c.body).actions[0].content, 'base64').toString('utf8'));
const commitsOf = (r) => r.w.calls.filter((c) => c.method === 'POST' && c.url.indexOf('/repository/commits') > 0);
const glCalls = (r, re) => r.w.calls.filter((c) => re.test(c.url));
const ENV_FORM = { env: { qa: { base_url: 'https://qa.example.com' }, stage: { base_url: 'https://stage.example.com' }, prod: { base_url: 'https://www.example.com' } },
  secrets: { davinci_variable_PingFedAuthNCreds_value: { ok: true } } };
const toOptions = [{ click: 'SEARCH', form: { query: 'registration' } }, { click: 'PICK:0', expect: /1 match/ }];

(async () => {
  // ---- access -----------------------------------------------------------------------------------
  {
    const r = await run({ worldOpt: { identity: {} } }, []);
    ok(/Sign-in required/.test(r.last) && r.pages.length === 1 && r.w.calls.length === 0, 'no identity from the sign-in subflow: nothing runs');
    common(r, 'no identity');
    const r2 = await run({ worldOpt: { identity: { userId: 'u2', username: 'eve', name: 'Eve', groups: ['Everyone'] } } }, []);
    ok(/do not have access/.test(r2.last) && r2.w.calls.length === 0, 'a user outside the allowed groups is refused');
    common(r2, 'not allowed');
    const r3 = await run({ settings: { ppGitlabProjectId: '', ppSourceEnvs: 'dev=nope' } }, []);
    ok(/not set up yet/.test(r3.last) && /ppGitlabProjectId: not set/.test(r3.last) && /ppSourceEnvs/.test(r3.last) && r3.last.indexOf('nope') < 0, 'settings problems name the variable, never its value');
    common(r3, 'settings');
    const r4 = await run({ settings: { ppAllowedGroups: '*' }, worldOpt: { identity: { userId: 'u3', username: 'any', name: 'Any', groups: '' } } }, [{ click: 'SIGNOUT' }]);
    ok(/You have left PingOne Promote/.test(r4.last), '* admits every signed-in user; leaving ends the flow');
    common(r4, 'star');
  }
  // ---- search: none, ambiguous, one, by ID, same env without calls, token failure --------------------
  {
    const r = await run({}, [
      { click: 'SEARCH', form: { query: 'zzz' }, expect: /New request/ },
      { click: 'SEARCH', form: { query: 'login' }, expect: /No flow in dev matches .zzz./ },
      { click: 'SEARCH', form: { query: 'registration' }, expect: /2 matches\. Choose one; the app never picks for you/ },
      { click: 'SEARCH', form: { query: dvid(4) }, expect: /1 match/ },
      { click: 'SEARCH', form: { query: 'evil' }, expect: /Password reset/ },
      { click: 'SEARCH', form: { query: 'global' }, expect: /evil/ },
      { click: 'SEARCH', form: { query: '' }, expect: /ppGitlabToken/ },
      { click: 'SIGNOUT', expect: /Type part of a flow name/ }
    ]);
    const lists = glCalls(r, /api\.pingone\.com/);
    eq(lists.length, 3, 'the flow list is read once (three pages) for all searches in one source env');
    eq(glCalls(r, /as\/token/).length, 1, 'one worker token for the session');
    const amb = r.pages.filter((p) => /2 matches/.test(p.html))[0].html;
    ok(buttonOf(amb, 'PICK:0') && buttonOf(amb, 'PICK:1') && !buttonOf(amb, 'PICK:2') && /Login - MFA/.test(amb), 'an ambiguous search lists every match to choose from');
    ok(r.budget.filter((b) => b.button === 'SEARCH').map((b) => b.calls).join(',') === '4,0,0,0,0,0,0', 'searches after the first make no calls');
    const evil = r.pages.filter((p) => /evil/.test(p.html))[0].html;
    ok(evil.indexOf('&lt;img src=x onerror=alert(1)&gt;') >= 0 && evil.indexOf('<img') < 0, 'a flow name with markup is shown as text');
    ok(r.pages.filter((p) => /ppGitlabToken/.test(p.html))[0].html.indexOf('&#123;&#123;global.company.variables.ppGitlabToken&#125;&#125;') >= 0, 'a flow name shaped like a reference cannot resolve');
    common(r, 'search');
    const r2 = await run({ worldOpt: { tokenStatus: 401 } }, [{ click: 'SEARCH', form: { query: 'x' } }, { click: 'SIGNOUT', expect: /did not let the app read flows \(HTTP 401\)/ }]);
    ok(r2.pages[1].html.indexOf(WORKER) < 0, 'a token failure message is redacted');
    common(r2, 'token failure');
    const r3 = await run({}, [{ click: 'SEARCH', form: { source: 'qa', query: 'login' } }, { click: 'SIGNOUT', expect: /No flow in qa matches/ }]);
    common(r3, 'empty env');
  }
  // ---- preview -> edit -> preview -> submit (MR) -> rollout with approval and failure ----------------------
  let mainRun;
  {
    const w = world();
    const r = await run({ world: w }, toOptions.concat([
      { click: 'PREVIEW', expect: /No preview yet/, form: { title: 'Bring Registration from dev', description: 'Ticket ABC-1' } },
      auto(/Preview$|Predicted changes/),
      { click: 'EDIT', expect: /Predicted changes 1 new, 1 changed in source, 1 differs; git version kept, 1 matches git/i },
      { click: 'PREVIEW', expect: /Keep Git version/, form: Object.assign({ deps: { 'pingone_davinci_connector_instance.http': 'update' } }, ENV_FORM) },
      auto(/Preview: ready/),
      { click: 'SUBMIT', expect: /qa ✓ Ready/ },
      auto(/Request submitted/),
      { click: 'WATCH', expect: /Open merge request/ },
      { do: (wd) => {
        const mr = wd.g.mrs[0];
        mr.state = 'merged'; mr.merge_commit_sha = newSha(); mr.merged_at = '2026-11-03T09:20:00Z';
        wd.g.nextPipe += 1;
        wd.g.pipelines.push({ id: wd.g.nextPipe, ref: 'main', sha: mr.merge_commit_sha, status: 'manual', jobs: [{ id: 1, name: 'plan:dev', status: 'success' }, { id: 2, name: 'apply:qa', status: 'success' }, { id: 3, name: 'apply:stage', status: 'manual' }, { id: 4, name: 'apply:prod', status: 'created' }] });
        wd.g.deployments.push({ id: 61, sha: mr.merge_commit_sha, status: 'blocked', created_at: '2026-11-03T09:25:00Z', environment: { name: 'stage' }, approvals: [{ status: 'approved' }], pending_approval_count: 1 });
      } },
      { auto: true, until: /Needs approval/, n: 0 },
      { do: (wd) => {
        const p = wd.g.pipelines[wd.g.pipelines.length - 1];
        p.jobs[2].status = 'success'; p.jobs[3].status = 'failed'; p.status = 'failed';
        wd.g.deployments[0].status = 'success';
      } },
      { click: 'REFRESH', expect: /qa ✓ Live stage ⏸ Needs approval 1 of 2 approvals prod ○ Waiting/ },
      { click: 'NAV_REQUESTS', expect: /stage ✓ Live prod ✕ Failed.*no more automatic checks/ },
      { click: 'OPEN:0', expect: /Requested by.*pat\.operator/ },
      { click: 'SIGNOUT', expect: /prod ✕ Failed/ }
    ]));
    mainRun = r;
    const cs = commitsOf(r);
    eq(cs.length, 3, 'preview, preview, submit: three request-file commits');
    const reqs = cs.map(decodeReq);
    const bodies = cs.map((c) => JSON.parse(c.body));
    ok(bodies[0].start_branch === 'main' && bodies[0].actions[0].action === 'create', 'the first preview creates the request branch from main');
    ok(bodies[1].actions[0].action === 'update' && bodies[2].actions[0].action === 'update' && !bodies[1].start_branch, 'later commits update the request file');
    eq(bodies.map((b) => b.branch), reqs.map((x) => x.branchName), 'every commit goes to the request branch named in request.json');
    eq(reqs.map((x) => x.dryRun), [true, true, false], 'Preview is dryRun true; Submit is dryRun false');
    eq(reqs.map((x) => x.requestId).filter((x, i, a) => a.indexOf(x) === i).length, 1, 'one requestId across preview and submit');
    ok(reqs.every((x) => x.requestedBy === 'pat.operator') && /Requested-by: pat\.operator/.test(bodies[2].commit_message), 'requestedBy in request.json and in the commit message');
    eq(reqs[2].dependencyChoices, { 'pingone_davinci_connector_instance.http': 'update' }, 'the dependency choice is sent');
    eq(reqs[2].envValues, ENV_FORM.env, 'environment values per rollout env are sent');
    eq(reqs[2].secretMappings, { davinci_variable_PingFedAuthNCreds_value: { varName: 'davinci_variable_PingFedAuthNCreds_value', field: 'pingfed_authn_creds' } }, 'secret mappings: names only');
    ok(reqs[0].title === 'Bring Registration from dev' && reqs[0].description === 'Ticket ABC-1' && reqs[0].mode === 'mr' && reqs[0].source === 'dev' && reqs[0].flows[0].name === 'Registration', 'request fields');
    ok(bodies[1].actions[0].last_commit_id && bodies[2].actions[0].last_commit_id && bodies[1].actions[0].last_commit_id !== bodies[2].actions[0].last_commit_id, 'each update names the previous request commit');
    const pv = r.pages.filter((p) => /Predicted changes/.test(p.html))[0];
    ok(/pingone_davinci_flow\.login.*selected flow/.test(visible(pv.html)) && /Readiness/.test(pv.html) && /base_url has no value for qa/.test(pv.html) && /davinci_variable_PingFedAuthNCreds_value/.test(pv.html), 'preview shows per-address changes, blockers and required secrets');
    ok(/Review the change in GitLab/.test(pv.html), 'preview links to the diff');
    ok(buttonOf(pv.html, 'SUBMIT').disabled && /Readiness: 3 environment\(s\) are missing values/.test(pv.html), 'blockers disable Submit, with the reason');
    const opt = r.pages.filter((p) => /Keep Git version/.test(p.html))[0].html;
    ok(/name="r\d+"[^>]*value="keep" checked/.test(opt), 'a dependency that differs defaults to Keep Git version (#93)');
    ok(!/pingone_davinci_flow\.login/.test(opt.split('Shared definitions')[1].split('Readiness')[0]), 'the selected flow has no keep/update choice');
    const working = r.pages.filter((p) => /running/.test(p.title + visible(p.html)) && autoOf(p.html));
    ok(working.length >= 3 && working.every((p) => /aria-live="polite"/.test(p.html)), 'working screens announce progress politely');
    ok(glCalls(r, /\/artifacts\/result\.json$/).length === 3, 'one result.json read per job');
    const strips = r.pages.filter((p) => /aria-label="Rollout"/.test(p.html)).map((p) => visible(p.html));
    ok(strips.some((v) => /qa ○ Waiting waits for the merge/.test(v)), 'before merge every env waits for the merge');
    ok(glCalls(r, /\/merge_requests\/[0-9]+$/).length >= 3 && glCalls(r, /ref=main/).length >= 2, 'status reads the MR and the post-merge pipeline on main');
    const firstMr = r.w.calls.indexOf(glCalls(r, /\/merge_requests\/[0-9]+$/)[0]);
    ok(firstMr > 0 && r.w.calls.slice(firstMr).every((c) => !/ref=pingone-promote/.test(c.url)), 'status never reads the request-branch (MR head) pipeline');
    const fin = r.pages.filter((p) => /no more automatic checks/.test(p.html))[0];
    ok(fin && !autoOf(fin.html), 'a finished rollout stops checking');
    common(r, 'main');
  }
  // ---- inputs as JSON text, artifact as text ---------------------------------------------------------------
  {
    const r = await run({ strings: true, worldOpt: { artifactAsText: true, noBlockers: true } }, toOptions.concat([
      { click: 'PREVIEW' }, auto(/Preview: ready/), { click: 'SUBMIT' }, auto(/Request submitted/), { click: 'SIGNOUT' }]));
    ok(/Request submitted/.test(r.pages[r.pages.length - 2].html), 'works with inputs fed as JSON text');
    common(r, 'strings');
  }
  // ---- branch mode ------------------------------------------------------------------------------------------
  {
    const r = await run({ worldOpt: { noBlockers: true } }, toOptions.concat([
      { click: 'PREVIEW', form: { mode: 'branch' } }, auto(/Preview: ready/),
      { click: 'SUBMIT', expect: /Submit request \(branch only\)/ }, auto(/Request submitted/),
      { click: 'WATCH', expect: /Open the branch/ },
      { do: (wd) => { wd.g.nextMr += 1; wd.g.mrs.push({ iid: wd.g.nextMr, title: 'opened by hand', state: 'opened', web_url: GLWEB + '/-/merge_requests/' + wd.g.nextMr, source_branch: Object.keys(wd.g.branches).filter((b) => b !== 'main')[0], description: '' }); } },
      { click: 'REFRESH', expect: /Branch only\. Nothing rolls out until someone opens and merges a merge request/ },
      { click: 'NAV_REQUESTS', expect: /Waiting for the merge request to be merged/ },
      { click: 'SIGNOUT', expect: /opened by hand/ }]));
    const reqs = commitsOf(r).map(decodeReq);
    ok(reqs[1].mode === 'branch' && !r.w.g.mrs[0].description, 'branch mode: no merge request from the job');
    ok(/Open the branch/.test(r.pages.filter((p) => /Request submitted/.test(p.html))[0].html), 'branch mode shows the branch link');
    common(r, 'branch');
  }
  // ---- typed errors on submit: conflict, preflight, partial, blocked; #57 ---------------------------------------------
  for (const code of ['conflict', 'preflight', 'partial', 'blocked']) {
    const r = await run({ worldOpt: { noBlockers: true, submitError: [code] } }, toOptions.concat([
      { click: 'PREVIEW' }, auto(/Preview: ready/),
      { click: 'SUBMIT' }, auto(/Submit failed/),
      { click: 'SUBMIT', force: true, expect: new RegExp(contract.ERROR_TEXT[code].title.split(':')[0]) },
      { click: 'EDIT', expect: /The last submit failed with these exact options/ },
      { click: 'SUBMIT', form: { title: 'Second try' }, expect: /Submit failed/ },
      auto(/Request submitted/), { click: 'SIGNOUT' }]));
    const cs = commitsOf(r);
    eq(cs.length, 3, code + ': the refused click made no commit; the edit allowed one more');
    const failed = r.pages.filter((p) => /Submit failed/.test(p.html))[0].html;
    ok(buttonOf(failed, 'SUBMIT').disabled, code + ': Submit is off after the error');
    ok(failed.indexOf(code + ' reported by the request job') >= 0, code + ': the job message is shown');
    if (code === 'partial') { ok(/Open the request branch/.test(failed), 'partial: the branch link is shown'); }
    ok(/Second try/.test(decodeReq(cs[2]).title) && decodeReq(cs[2]).dryRun === false, code + ': the edited options are submitted (#57)');
    common(r, code);
  }
  // ---- a typed error in a preview -----------------------------------------------------------------------------------
  {
    const r = await run({ worldOpt: { noBlockers: true, previewError: { code: 'preflight', message: 'terraform fmt failed for definitions/flows.tf' } } }, toOptions.concat([
      { click: 'PREVIEW' }, auto(/did not pass the checks/), { click: 'SIGNOUT', expect: /terraform fmt failed/ }]));
    ok(buttonOf(r.pages[r.pages.length - 2].html, 'SUBMIT').disabled, 'a failed preview disables Submit for the same options');
    common(r, 'preview preflight');
  }
  // ---- a pipeline that never appears: timeout, then Check GitLab recovers ----------------------------------------
  {
    const r = await run({ worldOpt: { noBlockers: true, submitPipelineDelay: 1e9 } }, toOptions.concat([
      { click: 'PREVIEW' }, auto(/Preview: ready/), { click: 'SUBMIT' }, auto(/GitLab is slow/),
      { click: 'CHECK', expect: /has not started a pipeline for the request after .*outcome of your submit is unknown/ },
      { do: (wd) => { wd.o.submitPipelineDelay = 0; } },
      auto(/Request submitted/), { click: 'SIGNOUT' }]));
    const slow = r.pages.filter((p) => /GitLab is slow/.test(p.html))[0].html;
    ok(!autoOf(slow) && buttonOf(slow, 'KEEP') && buttonOf(slow, 'CHECK'), 'after the timeout the app stops checking and offers Keep waiting and Check GitLab');
    ok(r.pages.some((p) => /GitLab has the request\. Waiting for its job/.test(p.html)), 'Check GitLab finds the request file and resumes waiting');
    eq(commitsOf(r).length, 2, 'Check GitLab never commits');
    common(r, 'never appears');
  }
  // ---- commit outcome unknown (502 after writing), then Check GitLab ------------------------------------------------
  {
    const r = await run({ worldOpt: { noBlockers: true, commitFail: [null, { status: 502, write: true }] } }, toOptions.concat([
      { click: 'PREVIEW' }, auto(/Preview: ready/), { click: 'SUBMIT' },
      { click: 'CHECK', expect: /did not confirm the commit \(HTTP 502\).*Outcome unknown/ },
      auto(/Request submitted/), { click: 'SIGNOUT' }]));
    const unk = r.pages.filter((p) => /Outcome unknown/.test(p.html))[0].html;
    ok(buttonOf(unk, 'SUBMIT').disabled && buttonOf(unk, 'PREVIEW').disabled && !buttonOf(unk, 'CANCEL'), 'unknown outcome: Preview, Submit and Cancel stay off until Check GitLab (#57)');
    eq(commitsOf(r).length, 2, 'the unknown submit is not repeated');
    common(r, 'unknown written');
  }
  // ---- commit outcome unknown and nothing was written ----------------------------------------------------------------
  {
    const r = await run({ worldOpt: { noBlockers: true, commitFail: [{ status: 504, write: false }] } }, toOptions.concat([
      { click: 'PREVIEW' },
      { click: 'CHECK', expect: /Outcome unknown/ },
      { click: 'PREVIEW', expect: /GitLab has no branch for this request: nothing was written/ },
      auto(/Preview: ready/), { click: 'SIGNOUT' }]));
    const bodies = commitsOf(r).map((c) => JSON.parse(c.body));
    ok(bodies.length === 2 && bodies[1].start_branch === 'main', 'after "nothing was written" the next preview creates the branch again');
    common(r, 'unknown not written');
  }
  // ---- GitLab refuses the commit (conflict) ----------------------------------------------------------------------------
  {
    const r = await run({ worldOpt: { noBlockers: true, commitFail: [null, { status: 400, write: false, message: 'You are attempting to update a file that has changed since you started editing it.' }] } }, toOptions.concat([
      { click: 'PREVIEW' }, auto(/Preview: ready/), { click: 'SUBMIT' },
      { click: 'SUBMIT', form: { title: 'Again' }, expect: /Submit failed: GitLab changed while this request was open/ },
      auto(/Request submitted/), { click: 'SIGNOUT' }]));
    common(r, 'commit conflict');
  }
  // ---- Stop waiting during a submit makes the outcome unknown --------------------------------------------------------
  {
    const r = await run({ worldOpt: { noBlockers: true } }, toOptions.concat([
      { click: 'PREVIEW' }, auto(/Preview: ready/), { click: 'SUBMIT' },
      { click: 'STOP', expect: /Submit running/ },
      { click: 'CHECK', expect: /You stopped waiting.*Outcome unknown/ },
      auto(/Request submitted/), { click: 'SIGNOUT' }]));
    common(r, 'stop');
  }
  // ---- slow runner ---------------------------------------------------------------------------------------------------------
  {
    const statuses = [];
    for (let i = 0; i < 25; i++) { statuses.push('pending'); }
    const r = await run({ worldOpt: { noBlockers: true, jobStatuses: statuses.concat(['running', 'success']) } }, toOptions.concat([
      { click: 'PREVIEW' }, auto(/No runner has picked up the job yet/), auto(/Preview: ready/), { click: 'SIGNOUT' }]));
    ok(r.pages.some((p) => /The job is waiting for a runner/.test(p.html)), 'a pending job reads as waiting for a runner');
    common(r, 'slow runner');
  }
  // ---- cancel: allowed while the head is the request commit, refused after -------------------------------------------
  {
    const r = await run({ worldOpt: { noBlockers: true } }, toOptions.concat([
      { click: 'PREVIEW' }, auto(/Preview: ready/), { click: 'CANCEL' }, { click: 'SIGNOUT', expect: /Request canceled: its branch pingone-promote\/.* was deleted/ }]));
    eq(r.w.g.deletes.length, 1, 'cancel deletes the request branch');
    ok(!r.w.g.branches[r.w.g.deletes[0]], 'the branch is gone');
    common(r, 'cancel');
    const r2 = await run({ worldOpt: { noBlockers: true } }, toOptions.concat([
      { click: 'PREVIEW' }, auto(/Preview: ready/),
      { do: (wd) => { const b = Object.keys(wd.g.branches).filter((x) => x !== 'main')[0]; wd.g.branches[b].commits.push(newSha()); wd.g.branches[b].titles.push('someone else'); } },
      { click: 'CANCEL' }, { click: 'SIGNOUT', expect: /Not canceled: the request job \(or someone\) has already committed/ }]));
    eq(r2.w.g.deletes.length, 0, 'cancel is refused once the branch has moved on');
    common(r2, 'cancel refused');
    const r3 = await run({ worldOpt: { noBlockers: true } }, toOptions.concat([
      { click: 'PREVIEW' }, auto(/Preview: ready/), { click: 'NAV_NEW' },
      { click: 'NAV_REQUESTS', expect: /still has a branch in GitLab/ },
      { click: 'CANCELB:0', expect: /Previewed, not submitted/ },
      { click: 'SIGNOUT', expect: /Request canceled.*No merge requests/ }]));
    eq(r3.w.g.deletes.length, 1, 'a previewed request can be canceled from Requests');
    common(r3, 'cancel from requests');
  }
  // ---- status: child pipeline, a canceled job that is live via a newer commit -------------------------------------------
  {
    const r = await run({ worldOpt: { noBlockers: true } }, toOptions.concat([
      { click: 'PREVIEW' }, auto(/Preview: ready/), { click: 'SUBMIT' }, auto(/Request submitted/),
      { do: (wd) => {
        const mr = wd.g.mrs[0];
        mr.state = 'merged'; mr.merge_commit_sha = newSha(); mr.merged_at = '2026-11-03T09:20:00Z';
        const newer = newSha();
        wd.g.mergeBase = mr.merge_commit_sha;
        wd.g.pipelines.push({ id: 801, ref: 'main', sha: mr.merge_commit_sha, status: 'canceled', bridges: [802], jobs: [{ id: 11, name: 'apply:qa', status: 'success' }, { id: 12, name: 'apply:stage', status: 'canceled' }] });
        wd.g.pipelines.push({ id: 802, child: true, ref: 'main', sha: mr.merge_commit_sha, status: 'canceled', jobs: [{ id: 13, name: 'apply:prod', status: 'canceled' }] });
        wd.g.deployments.push({ id: 71, sha: newer, status: 'success', created_at: '2026-11-03T10:00:00Z', environment: { name: 'stage' } });
      } },
      { click: 'WATCH' }, { click: 'SIGNOUT', expect: /qa ✓ Live stage ✓ Live via !77 prod – Canceled/ }]));
    ok(glCalls(r, /\/bridges/).length === 1 && glCalls(r, /pipelines\/802\/jobs/).length === 1, 'jobs in a child pipeline are found through its bridge');
    common(r, 'live via newer');
  }
  // ---- validator agrees -------------------------------------------------------------------------------------------------------
  {
    const perButton = {};
    for (const r of ALL_RUNS) { for (const b of r.budget) { const k = b.button.split(':')[0]; perButton[k] = Math.max(perButton[k] || 0, b.calls); } }
    console.log('max REST calls per browser request: ' + Object.keys(perButton).sort().map((k) => k + '=' + perButton[k]).join(' '));
  }
  console.log('ok: ' + passed + ' checks');
  Date.now = realNow;
})().catch((e) => { console.error(e); process.exit(1); });
