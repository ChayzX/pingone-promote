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
  pathFlows: '/v1/environments/{envId}/flows', rolloutEnvs: 'qa,stage,prod', gitlabApi: GLAPI, gitlabProjectId: PROJECT, allowedGroups: '*', pathApplications: 'none', pathFlowPolicies: 'none' }).cfg;
assert.throws(() => lib.gl(cfg0, 'deleteBranch', { branch: 'main' })); passed += 1;
assert.throws(() => lib.gl(cfg0, 'jobs', { id: '1/../../x' })); passed += 1;
assert.throws(() => lib.gl(cfg0, 'pipelines/1/play')); passed += 1;
assert.throws(() => lib.queue({ cfg: cfg0 }, 'x', [{ k: 'commit' }]), /only reads/); passed += 1;
eq(lib.webUrl(cfg0, 'https://evil.example/x'), '', 'links to other hosts are dropped');
eq(lib.webUrl(cfg0, 'javascript:alert(1)'), '', 'script links are dropped');
const baseSettings = { apiOrigin: 'https://api.pingone.com', authOrigin: 'https://auth.pingone.com', workerEnvironmentId: WORKER, sourceEnvs: 'dev=' + SRC,
  pathFlows: '/v1/environments/{envId}/flows', rolloutEnvs: 'qa,stage,prod', gitlabApi: GLAPI, gitlabProjectId: PROJECT, allowedGroups: '*' };
// Company variables are declared without values, so an empty one means its default (lib.DEFAULTS) and a re-import cannot overwrite a value.
const onlyIds = lib.checkSettings({ workerEnvironmentId: WORKER, sourceEnvs: 'dev=' + SRC, gitlabProjectId: PROJECT, allowedGroups: 'Promote Operators' });
ok(onlyIds.problems.length === 0 && onlyIds.cfg.apiOrigin === 'https://api.pingone.com' && onlyIds.cfg.rollout.join() === 'qa,stage,prod' && onlyIds.cfg.mrLabel === 'pingone-promote' &&
  onlyIds.cfg.gitlabApi === 'https://gitlab.com/api/v4' && onlyIds.cfg.runTimeoutSeconds === 600 && !!onlyIds.cfg.pathApplications, 'an app with only the ID and group settings set runs on the defaults');
eq(lib.checkSettings({ workerEnvironmentId: WORKER, sourceEnvs: 'dev=' + SRC, gitlabProjectId: PROJECT, allowedGroups: '*', pathApplications: 'none', flowsListQuery: 'none' }).cfg.pathApplications, '', '"none" switches an optional path off');
eq(Object.keys(lib.DEFAULTS).filter((k) => lib.SETTINGS.indexOf(k) < 0), [], 'every default belongs to a declared setting');
eq(flow.variables.filter((v) => v.context === 'company' && /^pp/.test(v.name) && lib.SETTINGS.map(lib.varName).indexOf(v.name.split('##SK##')[0]) >= 0).map((v) => v.fields.value).filter((x) => x !== ''), [], 'the export declares every setting without a value');
eq(flow.variables.filter((v) => v.context === 'flow').length, 0, 'no flow-scoped variable: configuration would be reset by a re-import');
eq(flow.variables.filter((v) => v.context === 'company').length, lib.SETTINGS.length + 2, 'company variables: every setting plus the two secrets');
eq(flow.variables.filter((v) => v.context === 'flowInstance').map((v) => v.name.split('##SK##')[0]).sort(), ['ppBody', 'ppFlows', 'ppLastFailed', 'ppState', 'ppUrl'], 'per-session state stays in flow-instance variables');
const PA = '/v1/environments/{envId}/davinciApplications';
const PP = '/v1/environments/{envId}/davinciApplications/{appId}/flowPolicies';
const cfgA = lib.checkSettings(Object.assign({}, baseSettings, { pathApplications: PA, pathFlowPolicies: PP }));
ok(cfgA.problems.length === 0 && !cfg0.pathApplications && !cfg0.pathFlowPolicies, 'the application and flow policy paths are optional settings; empty switches the step off');
eq(lib.listUrl(cfgA.cfg, { kind: 'applications', source: 'dev' }), 'https://api.pingone.com/v1/environments/' + SRC + '/davinciApplications', 'application list URL');
eq(lib.listUrl(cfgA.cfg, { kind: 'policies', source: 'dev', appId: dvid(21) }), 'https://api.pingone.com/v1/environments/' + SRC + '/davinciApplications/' + dvid(21) + '/flowPolicies', 'flow policy list URL for one application');
assert.throws(() => lib.listUrl(cfgA.cfg, { kind: 'policies', source: 'dev', appId: '../x' })); passed += 1;
assert.throws(() => lib.listUrl(cfg0, { kind: 'applications', source: 'dev' })); passed += 1;
for (const bad of [{ pathApplications: 'http://evil.example/x' }, { pathApplications: '/v1/{envId}/../x' }, { pathApplications: '/v1/environments/x/apps' }, { pathApplications: PA, pathFlowPolicies: '/v1/environments/{envId}/policies' },
  { pathApplications: PA, pathFlowPolicies: '/v1/environments/{envId}/{other}/{appId}' }]) {
  ok(lib.checkSettings(Object.assign({}, baseSettings, bad)).problems.some((x) => /^path(Applications|FlowPolicies)$/.test(x.name)), 'a bad list path is a settings problem: ' + JSON.stringify(bad).slice(0, 60));
}
const p1 = lib.checkSettings({});
eq(p1.problems.map((x) => x.name).sort(), ['allowedGroups', 'gitlabProjectId', 'sourceEnvs', 'workerEnvironmentId'], 'missing settings are listed: only those without a default');
const flowsFake = [{ id: 'a1', name: 'Login' }, { id: 'a2', name: 'Login - MFA' }, { id: 'a3', name: 'Registration' }];
eq(lib.searchFlows(flowsFake, 'LOGIN').matches.map((f) => f.id), ['a1', 'a2'], 'search is a case-insensitive substring; ambiguous lists all');
eq(lib.searchFlows(flowsFake, 'a3').matches.map((f) => f.id), ['a3'], 'search by exact ID');
eq(lib.searchFlows(flowsFake, 'zzz').total, 0, 'no match');
const req0 = contract.buildRequest({ requestId: rid, requestedBy: 'pat', source: 'dev', flow: { id: dvid(1), name: 'Login' },
  options: { mode: 'mr', title: 't', description: 'd', commitMessage: 'c', deps: {}, env: {}, secrets: {}, inline: [] }, dryRun: true });
// Every field the job's Request struct accepts (internal/request/request.go); unknown fields fail there.
const ALLOWED_KEYS = ['requestVersion', 'requestId', 'requestedBy', 'source', 'flows', 'applications', 'flowPolicies', 'dependencyChoices', 'envValues', 'secretMappings',
  'connectorEnvLocal', 'inlineValues', 'baseCommit', 'mode', 'branchName', 'commitMessage', 'title', 'description', 'dryRun'];
const REQUIRED_KEYS = ['requestVersion', 'requestId', 'requestedBy', 'source', 'flows', 'mode', 'branchName', 'dryRun'];
const keysOk = (r) => Object.keys(r).every((k) => ALLOWED_KEYS.indexOf(k) >= 0) && REQUIRED_KEYS.every((k) => lib.has(r, k));
ok(keysOk(req0) && !lib.has(req0, 'dependencyChoices') && !lib.has(req0, 'secretMappings') && !lib.has(req0, 'remove'), 'request.json: only known fields; empty optional ones are left out');
eq(req0.flows, [{ id: dvid(1), name: 'Login' }], 'a DaVinci flow ID is sent with the name');
eq(contract.buildRequest({ requestId: rid, requestedBy: 'p', source: 'dev', flow: { id: 'not-hex', name: 'X' }, options: { mode: 'mr', deps: {}, env: {}, secrets: {} }, dryRun: true }).flows,
  [{ name: 'X' }], 'a flow ID the job would refuse is left out; the name selects');
const cfgV = { rollout: ['qa', 'stage', 'prod'] };
eq(contract.validateRequest(req0, cfgV), [], 'a normal request passes the job rules');
const vbad = (patch, field) => {
  const r = Object.assign(JSON.parse(JSON.stringify(req0)), patch);
  const got = contract.validateRequest(r, cfgV).map((x) => x.field);
  ok(got.indexOf(field) >= 0, 'client-side check refuses ' + field + ' (' + JSON.stringify(patch).slice(0, 60) + ')');
};
vbad({ requestId: 'x' }, 'requestId');
vbad({ branchName: 'pingone-promote/other' }, 'branchName');
vbad({ source: 'qa' }, 'source');
vbad({ title: 'x'.repeat(256) }, 'title');
vbad({ title: 'a\nb' }, 'title');
vbad({ commitMessage: 'x'.repeat(2001) }, 'commitMessage');
vbad({ requestedBy: 'x'.repeat(129) }, 'requestedBy');
vbad({ flows: [] }, 'flows');
vbad({ flows: Array(21).fill({ name: 'a' }) }, 'flows');
vbad({ envValues: { other: { a: 'b' } } }, 'envValues.other');
vbad({ envValues: { qa: { import_ids: 'x' } } }, 'envValues.qa.import_ids');
vbad({ envValues: { qa: { key: GLTOKEN } } }, 'envValues.qa.key');
vbad({ secretMappings: { a: '1bad' } }, 'secretMappings.a');
vbad({ dependencyChoices: { 'not.an_address': 'keep' } }, 'dependencyChoices');
vbad({ inlineValues: ['1bad'] }, 'inlineValues');
vbad({ applications: Array(21).fill({ name: 'a' }) }, 'flows');
vbad({ flowPolicies: Array(21).fill({ name: 'a' }) }, 'flows');
vbad({ applications: [{ id: dvid(1), name: 'x' }] }, 'applications[0].id');
vbad({ flowPolicies: [{ name: ' ' }] }, 'flowPolicies[0].name');
eq(contract.validateRequest(Object.assign({}, req0, { flows: [], applications: [{ name: 'Web' }] }), cfgV), [], 'applications alone are enough, as in the job');
eq(contract.buildRequest({ requestId: rid, requestedBy: 'p', source: 'dev', flow: { id: dvid(1), name: 'X' }, options: { mode: 'mr', deps: {}, env: {}, secrets: {}, applications: [{ name: 'Web', app: 'x' }], flowPolicies: [{ name: 'P', app: 'Web' }] }, dryRun: true }).applications,
  [{ name: 'Web' }], 'applications and flow policies are sent by name only');
ok(contract.validateRequest(Object.assign({}, req0, { envValues: { dev: { a: 'b' }, prod: { uris: ['https://x.example.com/cb'] } } }), cfgV).length === 0, 'env values may name the source and rollout envs, and be JSON lists');
ok(lib.checkSettings({ apiOrigin: 'https://api.pingone.com', authOrigin: 'https://auth.pingone.com', workerEnvironmentId: WORKER, sourceEnvs: 'qa=' + SRC,
  pathFlows: '/v1/environments/{envId}/flows', rolloutEnvs: 'qa,prod', gitlabApi: GLAPI, gitlabProjectId: PROJECT, allowedGroups: '*' }).problems.some((x) => x.name === 'sourceEnvs'), 'a source env that is also in the rollout is a settings problem');
const cb = contract.commitBody(req0, 'main', '');
ok(cb.start_branch === 'main' && cb.actions[0].action === 'create' && !cb.actions[0].last_commit_id && cb.branch === 'pingone-promote/' + rid, 'first commit creates the branch');
ok(cb.commit_message.indexOf('PingOne-Promote-Request') < 0, 'the request commit never carries the job\'s idempotency trailer');
const cb2 = contract.commitBody(req0, 'main', 'f'.repeat(40));
ok(!cb2.start_branch && cb2.actions[0].action === 'update' && cb2.actions[0].last_commit_id === 'f'.repeat(40), 'later commits update with last_commit_id');
eq(JSON.parse(Buffer.from(cb.actions[0].content, 'base64').toString('utf8')), req0, 'the commit carries request.json as base64');
const nr = contract.normalizeResult({ resultVersion: 1, status: 'failed', diff: 'x'.repeat(5000), changes: [{ address: 'a.b', action: 'weird' }, {}, { address: 'pingone_x.y', action: 'moved', from: 'pingone_x.z', status: 'matches' }],
  blockers: { qa: ['x', { code: 'missing_value', var: 'v', message: 'm' }], prod: [], _request: [{ code: 'secret_finding', message: 'found' }] },
  requiredSecrets: [{ key: 'k', varName: 'k', field: 'f', declared: false, vault: 'apps/x/${VAULT_PATH_ENV}/f@secret' }],
  error: { code: 'nope', type: 'merge', message: 'm ' + GLTOKEN, retryable: true, field: 'flows[0]', candidates: [{ query: 'q', name: 'A', address: 'pingone_davinci_flow.a' }] } });
ok(nr.problems.length === 2 && nr.blockers.qa.length === 2 && !nr.blockers.prod && nr.blockers._request[0].code === 'secret_finding', 'blockers by env and _request');
ok(nr.requiredSecrets[0].declared === false && /VAULT_PATH_ENV/.test(nr.requiredSecrets[0].vault), 'required secrets keep declared and the vault path');
ok(nr.error.code === 'unknown' && nr.error.type === 'merge' && nr.error.retryable && nr.error.field === 'flows[0]' && nr.error.candidates[0].name === 'A' && nr.error.message.indexOf(GLTOKEN) < 0, 'typed error fields, redacted');
ok(nr.changes[1].action === 'moved' && nr.changes[1].from === 'pingone_x.z' && nr.summary.moved === 1 && JSON.stringify(nr).indexOf('xxxxx') < 0, 'moved rows keep from; the diff text is dropped');
ok(contract.normalizeResult({ resultVersion: 1, status: 'blocked', blockers: { qa: [{ code: 'missing_value', var: 'a', message: 'm' }] } }).error.code === 'blocked', 'status blocked reads as a blocked error');
const LOGLINE = (o) => contract.RESULT_LOG_PREFIX + JSON.stringify(Object.assign({ resultVersion: 1, requestId: rid, status: 'previewed', summary: { added: 2, unchanged: 1 } }, o));
const fromLog = (text) => contract.resultFromLog(text, rid);
eq(contract.TRACE_RANGE, 'bytes=-65536', 'the log is asked for by its last 64 KiB');
ok(fromLog('noise\n' + LOGLINE({ status: 'failed', error: { code: 'preflight', type: 'export', message: 'old' } }) + '\nnoise\n' + LOGLINE({}) + '\ndone\n').result.status === 'previewed', 'the LAST result line wins');
ok(fromLog('\u001b[0;32m' + LOGLINE({}) + '\u001b[0m\r\n').result.summary.added === 2, 'colour codes and CRLF around the line are ignored');
ok(fromLog('x'.repeat(70000) + '\n' + LOGLINE({})).result.status === 'previewed' && fromLog(LOGLINE({}) + '\n' + 'x'.repeat(70000)).result === null, 'only the last 64 KiB of the log is read');
eq(fromLog('Job succeeded\nPINGONE_PROMOTE_RESULT: no\n').why, 'the job log has no result line', 'no result line');
ok(/not valid/.test(fromLog('PINGONE_PROMOTE_RESULT {nope').why) && /not valid/.test(fromLog('PINGONE_PROMOTE_RESULT [1]').why) && /another request/.test(contract.resultFromLog(LOGLINE({}), 'other-request').why), 'a broken line or one for another request is not used');
const cl = fromLog(LOGLINE({ status: 'blocked', blockedEnvs: ['prod', 'bad env!'], mrUrl: GLWEB + '/-/merge_requests/9', error: { code: 'blocked', type: 'validation', message: 'm ' + GLTOKEN, retryable: false } })).result;
ok(cl.compact && cl.changes.length === 0 && cl.diffUrl === '' && Object.keys(cl.blockers).join() === 'prod' && cl.error.code === 'blocked' && cl.error.message.indexOf(GLTOKEN) < 0 && cl.summary.added === 2, 'the compact result: status, summary, blocked envs and a redacted error; no change list or diff');
ok(contract.useLogFallback(302) && contract.useLogFallback(301) && contract.useLogFallback(404) && !contract.useLogFallback(500) && !contract.useLogFallback(403) && !contract.useLogFallback(200), 'the log fallback is for a redirect or 404 only');
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
// The request job's own request rules (internal/request/request.go), written
// independently of fn/_contract.js so the two cross-check each other.
const JOB_RE = { requestId: /^[A-Za-z0-9][A-Za-z0-9_-]{2,63}$/, env: /^[A-Za-z0-9][A-Za-z0-9_-]{0,62}$/, ident: /^[A-Za-z_][A-Za-z0-9_]{0,127}$/,
  secretName: /^[A-Za-z][A-Za-z0-9_]{0,127}$/, address: /^pingone_[a-z0-9_]+\.[A-Za-z0-9_\-]{1,512}$/, id: /^[0-9a-fA-F]{32}$/ };
const JOB_SOURCES = ['dev', 'sandbox'];
const JOB_ROLLOUT = ['qa', 'stage', 'prod'];
function jobValidate(req, branch) {
  const fail = (field, msg) => ({ field: field, msg: msg });
  for (const k of Object.keys(req)) { if (ALLOWED_KEYS.indexOf(k) < 0) { return fail('', 'unknown field ' + k); } }
  if (req.requestVersion !== 1) { return fail('requestVersion', 'must be 1'); }
  if (!JOB_RE.requestId.test(req.requestId)) { return fail('requestId', 'bad'); }
  if (branch !== 'pingone-promote/' + req.requestId || (req.branchName && req.branchName !== branch)) { return fail('branchName', 'must be the request branch'); }
  if (typeof req.requestedBy !== 'string' || req.requestedBy.length > 128 || /[\n\t]/.test(req.requestedBy)) { return fail('requestedBy', 'bad'); }
  if (JOB_SOURCES.indexOf(req.source) < 0 || JOB_ROLLOUT.indexOf(req.source) >= 0) { return fail('source', 'not an allowed source'); }
  const kinds = [['flows', true], ['applications', false], ['flowPolicies', false]];
  if (kinds.reduce((n, k) => n + (req[k[0]] || []).length, 0) === 0) { return fail('flows', 'select at least one flow, application or flow policy'); }
  for (const [kind, allowId] of kinds) {
    const list = req[kind] || [];
    if (list.length > 20) { return fail('flows', 'at most 20 of each kind per request'); }
    for (const [i, f] of list.entries()) {
      if (!f.id && !f.name) { return fail(kind + '[' + i + ']', 'set id or name'); }
      if (f.id && (!allowId || !JOB_RE.id.test(f.id))) { return fail(kind + '[' + i + '].id', 'must be a DaVinci ID (flows only)'); }
      if (f.name !== undefined && (!f.name.trim() || f.name.length > 200 || /[\u0000-\u001f]/.test(f.name))) { return fail(kind + '[' + i + '].name', 'at most 200 printable characters'); }
      if (Object.keys(f).some((k) => ['id', 'name'].indexOf(k) < 0)) { return fail(kind + '[' + i + ']', 'unknown field'); }
    }
  }
  for (const v of req.inlineValues || []) { if (!JOB_RE.ident.test(v)) { return fail('inlineValues', v + ' is not a variable name'); } }
  if (lib.has(req, 'remove')) { return fail('', 'unknown field remove'); }
  for (const a of Object.keys(req.dependencyChoices || {})) { if (!JOB_RE.address.test(a) || ['update', 'keep'].indexOf(req.dependencyChoices[a]) < 0) { return fail('dependencyChoices', a); } }
  for (const env of Object.keys(req.envValues || {})) {
    if ([req.source].concat(JOB_ROLLOUT).indexOf(env) < 0) { return fail('envValues', env + ' is not the source or a rollout env'); }
    for (const k of Object.keys(req.envValues[env])) {
      const v = req.envValues[env][k];
      if (!JOB_RE.ident.test(k) || k === 'import_ids' || v === null) { return fail('envValues.' + env + '.' + k, 'bad'); }
      if (/glpat-|BEGIN/.test(JSON.stringify(v))) { return fail('envValues.' + env + '.' + k, 'looks like a secret'); }
    }
  }
  for (const k of Object.keys(req.secretMappings || {})) { if (!JOB_RE.ident.test(k) || typeof req.secretMappings[k] !== 'string' || !JOB_RE.secretName.test(req.secretMappings[k])) { return fail('secretMappings.' + k, 'keys and values must be variable names'); } }
  if (['mr', 'branch'].indexOf(req.mode) < 0) { return fail('mode', 'bad'); }
  if ((req.title || '').length > 255 || /\n/.test(req.title || '')) { return fail('title', 'one line of at most 255'); }
  if ((req.commitMessage || '').length > 2000) { return fail('commitMessage', 'at most 2000'); }
  return null;
}
const SEL = 'pingone_davinci_flow.pingcli__Registration';
const PREVIEW = (req, o) => {
  const env = req.envValues || {};
  const blockers = {};
  for (const e of JOB_ROLLOUT) {
    // inlineValues: the source value is a literal everywhere, so nothing is missing per environment.
    if ((req.inlineValues || []).indexOf('base_url') < 0 && !(env[e] && env[e].base_url)) { blockers[e] = [{ code: 'missing_value', var: 'base_url', message: e + '.tfvars must set base_url' }]; }
  }
  if (o.requestBlocker) { blockers._request = [{ code: 'customer_secrets', message: 'customer_secrets.yml is not valid YAML' }]; }
  const unchanged = o.noChanges;
  const changes = unchanged ? [{ address: SEL, role: 'selected', action: 'unchanged', status: 'matches', file: 'definitions/pingone_davinci_flow.tf' }] : [
    { address: SEL, role: 'selected', action: 'updated', status: 'changed-in-source', file: 'definitions/pingone_davinci_flow.tf' },
    { address: 'pingone_davinci_flow.pingcli__MFA-0020-sub', role: 'dependency', action: 'added', status: 'new', file: 'definitions/pingone_davinci_flow.tf' },
    { address: 'pingone_davinci_connector_instance.pingcli__Http', role: 'dependency', action: 'kept_differs', status: 'changed-in-source', reason: 'differs from Git; kept because it is a shared dependency (choose update to use the source version)' },
    { address: 'pingone_davinci_variable.pingcli__color', role: 'dependency', action: 'kept_differs', status: 'changed-in-both' },
    { address: 'pingone_davinci_variable.pingcli__theme_new', role: 'dependency', action: 'moved', status: 'matches', from: 'pingone_davinci_variable.pingcli__theme' },
    { address: 'pingone_davinci_variable.pingcli__lang', role: 'dependency', action: 'unchanged', status: 'matches' }];
  const summary = { added: 0, updated: 0, kept_differs: 0, unchanged: 0, moved: 0 };
  for (const c of changes) { summary[c.action] += 1; }
  const res = { resultVersion: 1, requestId: req.requestId, requestedBy: req.requestedBy, status: 'previewed', dryRun: true, mode: req.mode, source: req.source, branch: req.branchName,
    commitId: '', mrUrl: '', mrIid: 0, branchUrl: GLWEB + '/-/tree/' + encodeURIComponent(req.branchName), diffUrl: GLWEB + '/-/compare/main...' + encodeURIComponent(req.branchName),
    selected: [{ query: req.flows[0].id || req.flows[0].name, address: SEL, name: req.flows[0].name, id: req.flows[0].id }]
      .concat((req.applications || []).map((a) => ({ query: a.name, address: 'pingone_davinci_application.pingcli__' + a.name.replace(/[^A-Za-z0-9]/g, '_'), name: a.name })))
      .concat((req.flowPolicies || []).map((a) => ({ query: a.name, address: 'pingone_davinci_application_flow_policy.pingcli__' + a.name.replace(/[^A-Za-z0-9]/g, '_'), name: a.name }))), changes: changes, summary: summary, files: [],
    requiredSecrets: [{ key: 'davinci_variable_PingFedAuthNCreds_value', varName: 'davinci_variable_PingFedAuthNCreds_value', field: 'davinci_variable_PingFedAuthNCreds_value', declared: false,
      vault: 'apps/pingone-davinci/${VAULT_PATH_ENV}/pingone/davinci_variable_PingFedAuthNCreds_value@secret' }],
    blockers: blockers, warnings: o.warning ? ['flows[0]: name does not match ' + SRC] : [], diff: '--- a/definitions/pingone_davinci_flow.tf\n' + '+ x\n'.repeat(4000), diffTruncated: false, error: null };
  if (o.noBlockers) { res.blockers = o.requestBlocker ? { _request: blockers._request } : {}; res.requiredSecrets = []; }
  if (Object.keys(res.blockers).length) { res.status = 'blocked'; res.error = { code: 'blocked', type: 'validation', message: 'readiness blockers', retryable: false }; }
  return res;
};

function world(opt) {
  const o = Object.assign({ pageSize: 3, tokenStatus: 200, pipelineDelay: 0, submitPipelineDelay: 0, jobStatuses: ['pending', 'running', 'success'], submitError: null,
    commitFail: null, noBlockers: false, artifactAsText: false, identity: { userId: 'u-1', username: 'pat.operator', name: 'Pat Operator', groups: ['Promote Operators'] } }, opt || {});
  const flows = makeFlows();
  const calls = [];
  const g = { branches: { main: { commits: [newSha()] } }, files: {}, pipelines: [], mrs: [], deployments: [], commits: [], nextPipe: 500, nextJob: 9000, nextMr: 40, deletes: [], jobRefusals: [], submitted: {}, traces: [] };
  const head = (b) => g.branches[b].commits[g.branches[b].commits.length - 1];
  const branchObj = (b) => ({ name: b, web_url: GLWEB + '/-/tree/' + b, commit: { id: head(b), title: g.branches[b].titles[g.branches[b].titles.length - 1], committed_date: '2026-11-03T09:00:00Z' } });
  g.branches.main.titles = ['init'];
  const commitTo = (b, t) => { const s = newSha(); g.branches[b].commits.push(s); g.branches[b].titles.push(t); return s; };
  const page = (items, pg, base, key, query) => {
    const start = pg * o.pageSize;
    const body = { _embedded: { [key || 'flows']: items.slice(start, start + o.pageSize) }, count: items.length };
    if (start + o.pageSize < items.length) { body._links = { next: { href: base + (query === undefined ? '?attributes=id,name&' : '?') + 'cursor=' + (pg + 1) } }; }
    return { status: 200, body: body };
  };
  // Applications carry more than names: a client secret that must never reach the state or a screen.
  const apps = (o.manyApps ? Array.from({ length: 25 }, (_, i) => ({ id: dvid(100 + i), name: 'Bulk app ' + String(i + 1).padStart(2, '0') })) : [
    { id: dvid(21), name: 'Web Portal' }, { id: dvid(22), name: 'Mobile App' }, { id: dvid(23), name: 'Mobile App Beta' }, { id: dvid(24), name: 'Admin Console' },
    { id: dvid(25), name: 'Partner API' }, { id: dvid(26), name: 'Partner API' }, { id: dvid(27), name: '<img src=x onerror=alert(1)> app' }, { id: dvid(28), name: 'Kiosk' }])
    .map((a) => Object.assign({ oauth: { clientSecret: 'secret-value-for-tests', redirectUris: ['https://x.example.com/cb'] } }, a));
  const policiesOf = { [dvid(21)]: ['Sign in policy', 'Sign in policy - MFA', 'Registration policy', 'Recovery policy'], [dvid(28)]: ['Kiosk policy'] };
  const runJob = (p) => {
    // The request job finishes: decide the result and do what the backend would do.
    const req = p.req;
    const failed = (code, type, message, extra) => Object.assign(PREVIEW(req, o), { status: 'failed', dryRun: req.dryRun, changes: [], blockers: {},
      error: Object.assign({ code: code, type: type, message: message, retryable: false }, extra || {}) });
    const bad = jobValidate(req, p.ref);
    let res;
    if (bad) {
      g.jobRefusals.push(bad);
      res = failed('preflight', 'validation', 'request: ' + bad.field + ': ' + bad.msg, { field: bad.field });
    } else if (req.dryRun) {
      res = PREVIEW(req, o);
      if (o.previewError) { res = failed(o.previewError.code, o.previewError.type || 'export', o.previewError.message, o.previewError); }
    } else if (o.submitError && o.submitError.length) {
      const e = o.submitError.shift();
      const code = typeof e === 'string' ? e : e.code;
      res = failed(code, code === 'conflict' ? 'gitlab' : 'merge', code + ' reported by the request job for ' + req.branchName, typeof e === 'string' ? {} : e);
      if (code === 'partial') { commitTo(req.branchName, 'pingone-promote: generated change'); res.branchUrl = GLWEB + '/-/tree/' + req.branchName; }
      if (code === 'blocked') { res.status = 'blocked'; res.blockers = { prod: [{ code: 'missing_value', var: 'base_url', message: 'prod.tfvars must set base_url' }] }; }
    } else if (o.noChanges) {
      commitTo(req.branchName, 'pingone-promote: delete request file');
      delete g.files[req.branchName];
      res = Object.assign(PREVIEW(req, o), { status: 'no_changes', dryRun: false, blockers: {}, error: null });
    } else {
      const prior = g.mrs.filter((m) => m.source_branch === req.branchName)[0];
      res = Object.assign(PREVIEW(req, o), { dryRun: false, blockers: {}, error: null, diff: 'x'.repeat(1000) });
      if (prior || g.submitted[req.requestId]) {
        // The PingOne-Promote-Request trailer is already in the branch history: report it, write nothing.
        res.status = 'already_submitted';
        res.commitId = g.submitted[req.requestId];
        if (prior) { res.mrUrl = prior.web_url; res.mrIid = prior.iid; }
      } else {
        const sha = commitTo(req.branchName, 'PingOne: ' + req.flows[0].name + ' from ' + req.source);
        g.submitted[req.requestId] = sha;
        delete g.files[req.branchName];
        res.status = 'submitted';
        res.commitId = sha;
        res.diffUrl = GLWEB + '/-/commit/' + sha;
        if (req.mode === 'mr') {
          g.nextMr += 1;
          g.mrs.push({ iid: g.nextMr, title: req.title, state: 'opened', web_url: GLWEB + '/-/merge_requests/' + g.nextMr, source_branch: req.branchName,
            description: req.description + '\n\n| change table |\n\nRequested-by: ' + req.requestedBy + '\nRequest-id: ' + req.requestId + '\n', labels: ['pingone-promote'], updated_at: '2026-11-03T09:10:00Z' });
          res.mrUrl = GLWEB + '/-/merge_requests/' + g.nextMr;
          res.mrIid = g.nextMr;
        }
      }
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
      const ma = /^https:\/\/api\.pingone\.com\/v1\/environments\/([^/]+)\/davinciApplications(?:\?cursor=([0-9]+))?$/.exec(url);
      if (ma) { return page(ma[1] === SRC ? apps : [], Number(ma[2] || 0), 'https://api.pingone.com/v1/environments/' + ma[1] + '/davinciApplications', 'davinciApplications', ''); }
      const mp = /^https:\/\/api\.pingone\.com\/v1\/environments\/([^/]+)\/davinciApplications\/([A-Za-z0-9_-]+)\/flowPolicies(?:\?cursor=([0-9]+))?$/.exec(url);
      if (mp) {
        const list = (policiesOf[mp[2]] || []).map((n, i) => ({ id: dvid(200 + i), name: n, status: 'enabled' }));
        return page(mp[1] === SRC ? list : [], Number(mp[3] || 0), 'https://api.pingone.com/v1/environments/' + mp[1] + '/davinciApplications/' + mp[2] + '/flowPolicies', 'flowPolicies', '');
      }
      const m = /^https:\/\/api\.pingone\.com\/v1\/environments\/([^/]+)\/flows\?attributes=id,name(?:&cursor=([0-9]+))?$/.exec(url);
      assert.ok(m, 'only the flow, application and flow policy lists are read: ' + url);
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
    if (method === 'DELETE' && o.protectedBranches) { return { status: 403, body: { message: '403 Forbidden' } }; }
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
      let st2 = st;
      if (['success', 'failed', 'canceled'].indexOf(st) >= 0 && !p.result) {
        runJob(p);
        // blocked and failed exit 1, so the job and pipeline fail; result.json is still uploaded.
        if (['blocked', 'failed'].indexOf(p.result.status) >= 0) { j.statuses = j.statuses.slice(0, j.i).concat(['failed']); st2 = 'failed'; }
        p.status = st2;
      }
      if (p.result && ['blocked', 'failed'].indexOf(p.result.status) >= 0) { st2 = 'failed'; }
      return { status: 200, body: [{ id: 1, name: 'lint', status: 'success' }, { id: j.id, name: 'pingone-promote:request', status: st2, started_at: '2026-11-03T09:00:05Z', web_url: GLWEB + '/-/jobs/' + j.id, pipeline: { id: p.id, status: p.status } }] };
    }
    if ((m = /^\/pipelines\/([0-9]+)\/bridges$/.exec(bare))) {
      const p = g.pipelines.filter((x) => x.id === Number(m[1]))[0];
      return { status: 200, body: (p.bridges || []).map((id) => ({ id: 1, name: 'child', downstream_pipeline: { id: id } })) };
    }
    if ((m = /^\/jobs\/([0-9]+)\/artifacts\/result\.json$/.exec(bare))) {
      const p = g.pipelines.filter((x) => x.job && x.job.id === Number(m[1]))[0];
      if (!p || !p.result || o.noArtifact) { return { status: 404, body: { message: '404 Not Found' } }; }
      // Some GitLab setups redirect artifact downloads to object storage; the REST node does not follow it.
      if (o.artifactStatus) { return { status: o.artifactStatus, body: '' }; }
      return { status: 200, body: o.artifactAsText ? JSON.stringify(p.result) : JSON.parse(JSON.stringify(p.result)) };
    }
    if ((m = /^\/jobs\/([0-9]+)\/trace$/.exec(bare))) {
      const p = g.pipelines.filter((x) => x.job && x.job.id === Number(m[1]))[0];
      assert.strictEqual(headers.Accept, 'text/plain');
      g.traces.push({ range: headers.Range, job: Number(m[1]) });
      if (!p || !p.result) { return { status: 404, body: { message: '404 Not Found' } }; }
      if (o.traceStatus) { return { status: o.traceStatus, body: 'LOGNOISE gateway error' }; }
      const res = p.result;
      const last = 'PINGONE_PROMOTE_RESULT ' + JSON.stringify({ resultVersion: res.resultVersion, requestId: res.requestId, status: res.status, mrUrl: res.mrUrl || undefined, branchUrl: res.branchUrl || undefined,
        commitId: res.commitId || undefined, summary: res.summary, blockedEnvs: Object.keys(res.blockers).sort(), error: res.error || undefined });
      const stale = 'PINGONE_PROMOTE_RESULT ' + JSON.stringify({ resultVersion: 1, requestId: res.requestId, status: 'failed', summary: {}, error: { code: 'preflight', type: 'export', message: 'an earlier line', retryable: false } });
      // The log: a long head (outside the last 64 KiB), noise, an older result line, noise and the real last line.
      const none = o.traceMode === 'none';
      const text = [none ? 'LOGNOISE start' : 'PINGONE_PROMOTE_RESULT {"status":"failed"}', 'LOGNOISE-head '.repeat(8000), '\u001b[0;32mRunning pingone-promote request\u001b[0m LOGNOISE-token', none ? 'LOGNOISE' : stale,
        'LOGNOISE terraform fmt done', none ? 'Job succeeded LOGNOISE' : '\u001b[0;m' + last, none ? '' : 'Cleaning up project directory LOGNOISE'].join('\r\n');
      const ranged = headers.Range === 'bytes=-65536' && !o.traceIgnoreRange;
      return { status: ranged ? 206 : 200, body: ranged ? text.slice(-65536) : text };
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

const SETTINGS = { ppWorkerEnvironmentId: WORKER, ppSourceEnvs: 'dev=' + SRC + ', sandbox=' + QAENV, ppGitlabApi: GLAPI, ppGitlabProjectId: PROJECT, ppAllowedGroups: 'Promote Operators' };
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
  // Company variables are what DaVinci keeps across re-imports: the settings the operator set (empty in the export, so defaults
  // apply for the rest) and the two secrets. Nothing is flow-scoped, and the flow can only write flow-instance variables.
  const company = o.sharedCompany || {};
  for (const v of flow.variables) {
    assert.ok(v.context !== 'flow', 'no flow-scoped variable in the export: ' + v.name);
    if (v.context === 'company') { company[v.name.split('##SK##')[0]] = v.fields.value; }
  }
  Object.assign(company, SETTINGS, o.settings, { ppWorkerBasicAuth: BASIC, ppGitlabToken: GLTOKEN });
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
    assert.ok(p[1] !== 'flow', 'reads a flow-scoped variable: ' + ref);
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
  if (!r.allowRefusal) { eq(r.w.g.jobRefusals, [], label + ': the request job accepted every request (client-side checks match its rules)'); }
  ok(JSON.stringify(r.inst.ppState || {}).length < 40000, label + ': the flow-instance state stays small (no diff text)');
  for (const pg of r.pages) {
    const v = visible(pg.html);
    for (const s of SECRETS) { ok(pg.html.indexOf(s) < 0, label + ': no secret or token on screen "' + pg.title + '"'); }
    ok(pg.html.indexOf(SRC) < 0 && pg.html.indexOf(WORKER) < 0 && pg.html.indexOf(QAENV) < 0, label + ': no environment ID on screen');
    ok(pg.html.indexOf('LOGNOISE') < 0, label + ': no raw log text on screen "' + pg.title + '"');
    ok(!BANNED.test(v), label + ': vocabulary on "' + pg.title + '": ' + (BANNED.exec(v) || [''])[0]);
    ok(!/\bplan\b/i.test(v.replace(/pipeline plan job/gi, '')), label + ': "plan" only as "pipeline plan job"');
    ok(!/<script|onerror=/i.test(pg.html.replace(/&lt;img src=x onerror=alert\(1\)&gt;/g, '')), label + ': no markup injection on screen');
  }
  const st = JSON.stringify(r.inst.ppState || {}) + JSON.stringify(r.inst.ppFlows || {}) + String(r.inst.ppBody || '') + String(r.inst.ppUrl || '');
  for (const s of SECRETS) { ok(st.indexOf(s) < 0, label + ': no secret or token in flow-instance state'); }
  ok(st.indexOf('LOGNOISE') < 0, label + ': no log text in flow-instance state');
  for (const c of r.w.calls) {
    if (c.url.indexOf('gitlab') > 0) { ok(c.headers.Authorization === undefined && JSON.stringify(c.body || '').indexOf(ACCESS) < 0 && JSON.stringify(c.body || '').indexOf(BASIC) < 0, label + ': PingOne credentials never go to GitLab'); }
    else { ok(JSON.stringify(c).indexOf(GLTOKEN) < 0, label + ': the GitLab token never goes to PingOne'); }
    if (c.method === 'POST' && c.url.indexOf('gitlab') > 0) {
      const req = Buffer.from(JSON.parse(c.body).actions[0].content, 'base64').toString('utf8');
      for (const s of SECRETS) { ok(req.indexOf(s) < 0 && c.body.indexOf(s) < 0, label + ': no secret in request.json or the commit'); }
      ok(keysOk(JSON.parse(req)), label + ': request.json has only the job\'s fields');
    }
  }
  const LIMITS = { START: 0, SEARCH: 2 + 3, SEARCH_APP: 2 + 3, POLICIES: 2 + 2, POLL: 4, KEEP: 4, PREVIEW: 1, SUBMIT: 1, CHECK: 5, CANCEL: 2, CANCELB: 2, REFRESH: lib.MAX_CALLS, RESUME: lib.MAX_CALLS, WATCH: lib.MAX_CALLS, NAV_REQUESTS: 2 };
  for (const b of r.budget) {
    const key = b.button.split(':')[0].replace(/^OPENB?$/, 'WATCH');
    const lim = lib.has(LIMITS, key) ? LIMITS[key] : 0;
    ok(b.calls <= lim, label + ': ' + b.button + ' made ' + b.calls + ' REST calls (budget ' + lim + ')');
  }
}
const decodeReq = (c) => JSON.parse(Buffer.from(JSON.parse(c.body).actions[0].content, 'base64').toString('utf8'));
const commitsOf = (r) => r.w.calls.filter((c) => c.method === 'POST' && c.url.indexOf('/repository/commits') > 0);
const glCalls = (r, re) => r.w.calls.filter((c) => re.test(c.url));
const ENV_FORM = { env: { qa: { base_url: 'https://qa.example.com' }, stage: { base_url: 'https://stage.example.com' }, prod: { base_url: '["https://www.example.com","https://example.com"]' } },
  secrets: { davinci_variable_PingFedAuthNCreds_value: { ok: true, mapTo: 'PingFedAuthNCreds' } } };
const HTTP = 'pingone_davinci_connector_instance.pingcli__Http';
const COLOR = 'pingone_davinci_variable.pingcli__color';
const LANG = 'pingone_davinci_variable.pingcli__lang';
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
  // ---- two concurrent sessions share the company variables, never the state -------------------------------------
  {
    const shared = {};
    const A = { identity: { userId: 'u-a', username: 'alice.a', name: 'Alice Anders', groups: ['Promote Operators'] } };
    const B = { identity: { userId: 'u-b', username: 'bob.b', name: 'Bob Brandt', groups: ['Promote Operators'] } };
    const [ra, rb] = await Promise.all([
      run({ sharedCompany: shared, worldOpt: A }, [{ click: 'SEARCH', form: { query: 'registration' } }, { click: 'PICK:0', expect: /1 match/ }, { click: 'PREVIEW' }, auto(/Predicted changes/), { click: 'SIGNOUT' }]),
      run({ sharedCompany: shared, worldOpt: B }, [{ click: 'SEARCH', form: { query: 'password' } }, { click: 'PICK:0', expect: /1 match/ }, { click: 'SIGNOUT' }])]);
    const stateOf = (r) => JSON.stringify(r.pages.map((p) => p.state));
    ok(/Alice Anders/.test(visible(ra.pages[0].html)) && /Bob Brandt/.test(visible(rb.pages[0].html)), 'each session shows its own user');
    ok(ra.pages.every((p) => p.html.indexOf('Bob') < 0 && p.html.indexOf('bob.b') < 0 && stateOf(ra).indexOf('u-b') < 0), 'session A never sees session B');
    ok(rb.pages.every((p) => p.html.indexOf('Alice') < 0 && p.html.indexOf('alice.a') < 0) && stateOf(rb).indexOf('u-a') < 0, 'session B never sees session A');
    ok(stateOf(ra).indexOf('Registration') > 0 && stateOf(rb).indexOf('Registration') < 0 && stateOf(rb).indexOf('Password reset') > 0, 'each session keeps its own selected flow');
    ok(JSON.stringify(ra.inst) !== JSON.stringify(rb.inst) && ra.inst.ppState.req.requestId !== rb.inst.ppState.req.requestId || !rb.inst.ppState.req, 'the flow-instance state of the two sessions differs');
    eq(Object.keys(shared).sort(), Object.keys(shared).filter((k) => /^pp/.test(k)).sort(), 'only pp settings and secrets are company variables');
    ok(!('ppState' in shared) && !('ppFlows' in shared) && !('ppUrl' in shared) && !('ppBody' in shared) && !('ppLastFailed' in shared), 'no per-session state is stored in a company variable');
    common(ra, 'concurrent A'); common(rb, 'concurrent B');
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
    const r3 = await run({}, [{ click: 'SEARCH', form: { source: 'sandbox', query: 'login' } }, { click: 'SIGNOUT', expect: /No flow in sandbox matches/ }]);
    common(r3, 'empty env');
  }
  // ---- preview -> edit -> preview -> submit (MR) -> rollout with approval and failure ----------------------
  let mainRun;
  {
    const w = world();
    const r = await run({ world: w }, toOptions.concat([
      { click: 'PREVIEW', expect: /No preview yet/, form: { title: 'Bring Registration from dev', description: 'Ticket ABC-1' } },
      auto(/Preview$|Predicted changes/),
      { click: 'EDIT', expect: /Predicted changes 1 new, 1 changed in source, 2 differs; git version kept, 1 moved \(new address\), 1 matches git/i },
      { click: 'PREVIEW', expect: /Keep Git version/, form: Object.assign({ deps: { [HTTP]: 'update' } }, ENV_FORM) },
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
    eq(reqs[2].dependencyChoices, { [HTTP]: 'update', [COLOR]: 'keep' }, 'dependency choices are sent: the changed one, and the changed-in-both default');
    eq(reqs[2].envValues, { qa: ENV_FORM.env.qa, stage: ENV_FORM.env.stage, prod: { base_url: ['https://www.example.com', 'https://example.com'] } }, 'environment values per rollout env are sent; JSON text becomes a list');
    eq(reqs[2].secretMappings, { davinci_variable_PingFedAuthNCreds_value: 'PingFedAuthNCreds' }, 'secret mappings: terraformer name to customer_secrets name');
    ok(reqs[0].title === 'Bring Registration from dev' && reqs[0].description === 'Ticket ABC-1' && reqs[0].mode === 'mr' && reqs[0].source === 'dev' && reqs[0].flows[0].name === 'Registration', 'request fields');
    ok(bodies[1].actions[0].last_commit_id && bodies[2].actions[0].last_commit_id && bodies[1].actions[0].last_commit_id !== bodies[2].actions[0].last_commit_id, 'each update names the previous request commit');
    const pv = r.pages.filter((p) => /Predicted changes/.test(p.html))[0];
    ok(/pingcli__Registration selected flow/.test(visible(pv.html)) && /Readiness/.test(pv.html) && /qa\.tfvars must set base_url/.test(pv.html) && /davinci_variable_PingFedAuthNCreds_value/.test(pv.html), 'preview shows per-address changes, blockers and required secrets');
    ok(/Review the change in GitLab/.test(pv.html), 'preview links to the diff');
    ok(buttonOf(pv.html, 'SUBMIT').disabled && /Readiness: 3 environment\(s\) are missing values/.test(pv.html), 'blockers disable Submit, with the reason');
    const opt = r.pages.filter((p) => /Keep Git version/.test(p.html))[0].html;
    ok(/name="r\d+"[^>]*value="keep" checked/.test(opt), 'a dependency that differs defaults to Keep Git version (#93)');
    const depPart = opt.split('Shared definitions')[1].split('Readiness')[0];
    ok(!/pingcli__Registration/.test(depPart) && /changed in both Git and the source/.test(depPart), 'the selected flow has no choice; a changed-in-both address does');
    ok(/moved \(new address\).*was pingone_davinci_variable\.pingcli__theme/i.test(visible(pv.html).replace(/\s+/g, ' ')) || /was pingone_davinci_variable\.pingcli__theme/.test(visible(pv.html)), 'a moved row shows its old address');
    ok(/declared|customer_secrets\.yml/.test(pv.html) && /VAULT_PATH_ENV/.test(visible(pv.html)), 'required secrets show the Vault path and that the request adds them');
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
  // ---- final contract: ambiguous name with candidates ---------------------------------------------------------------
  {
    const r = await run({ worldOpt: { noBlockers: true, previewError: { code: 'preflight', type: 'validation', message: 'flows[0]: "Reg" matches 2 flows', retryable: false, field: 'flows[0]',
      candidates: [{ query: 'Reg', name: 'Registration', address: SEL }, { query: 'Reg', name: 'Registration v2', address: 'pingone_davinci_flow.pingcli__Registration-0020-v2' }] } } }, toOptions.concat([
      { click: 'PREVIEW' }, auto(/did not pass the checks/),
      { click: 'SIGNOUT', expect: /Several flows match\. Search again and choose one of these: Registration .*Registration v2.*Field: flows\[0\].*\(in the request itself\)|Several flows match/ }]));
    const pv = r.pages[r.pages.length - 2].html;
    ok(/Registration v2/.test(pv) && /Several flows match/.test(pv) && /Field: <code>flows\[0\]<\/code>/.test(pv), 'an ambiguous name shows the candidates and the field');
    common(r, 'candidates');
  }
  // ---- already_submitted: the existing MR is shown, nothing new is opened ------------------------------------------------
  {
    const r = await run({ worldOpt: { noBlockers: true } }, toOptions.concat([
      { click: 'PREVIEW' }, auto(/Preview: ready/),
      { do: (wd) => { const b = Object.keys(wd.g.branches).filter((x) => x !== 'main')[0]; wd.g.nextMr += 1; wd.g.mrs.push({ iid: wd.g.nextMr, title: 'earlier', state: 'opened', web_url: GLWEB + '/-/merge_requests/' + wd.g.nextMr, source_branch: b, description: '' }); } },
      { click: 'SUBMIT' }, auto(/Request submitted/),
      { click: 'WATCH', expect: /This request was already submitted\. Nothing new was written; this is the existing merge request/ },
      { click: 'SIGNOUT', expect: /Waiting for the merge request to be merged/ }]));
    eq(r.w.g.mrs.length, 1, 'already_submitted: no second merge request');
    common(r, 'already submitted');
  }
  // ---- no_changes: preview says so and Submit is off; a direct submit ends with "No changes" -------------------------------
  {
    const r = await run({ worldOpt: { noBlockers: true, noChanges: true } }, toOptions.concat([
      { click: 'PREVIEW' }, auto(/No changes: Git already matches dev/),
      { click: 'SIGNOUT', expect: /Nothing to change: Git already matches the source/ }]));
    ok(buttonOf(r.pages[r.pages.length - 2].html, 'SUBMIT').disabled, 'no changes: Submit is off');
    common(r, 'no changes preview');
    const r2 = await run({ worldOpt: { noBlockers: true, noChanges: true } }, toOptions.concat([
      { click: 'SUBMIT' }, auto(/^.*No changes Git already matches dev/), { click: 'SIGNOUT', expect: /wrote nothing and opened no merge request/ }]));
    ok(!buttonOf(r2.pages[r2.pages.length - 2].html, 'WATCH') && r2.w.g.mrs.length === 0, 'no_changes ends the request without a rollout to watch');
    common(r2, 'no changes submit');
  }
  // ---- a retryable error needs no edit ---------------------------------------------------------------------------------------
  {
    const r = await run({ worldOpt: { noBlockers: true, submitError: [{ code: 'conflict', type: 'gitlab', retryable: true }] } }, toOptions.concat([
      { click: 'PREVIEW' }, auto(/Preview: ready/), { click: 'SUBMIT' }, auto(/Submit failed/),
      { click: 'SUBMIT', expect: /GitLab says this may work if you try again.*You can submit again as it is/ }, auto(/Request submitted/), { click: 'SIGNOUT' }]));
    eq(commitsOf(r).length, 3, 'a retryable error allows the same submit again');
    common(r, 'retryable');
  }
  // ---- the job names a field: it is pointed out next to that field -------------------------------------------------------------
  {
    const r = await run({ worldOpt: { noBlockers: true, submitError: [{ code: 'preflight', type: 'validation', field: 'title', message: 'title must not mention prod' }] } }, toOptions.concat([
      { click: 'PREVIEW' }, auto(/Preview: ready/), { click: 'SUBMIT' }, auto(/Submit failed/),
      { click: 'EDIT' },
      { click: 'SUBMIT', form: { title: 'Registration for the rollout' }, expect: /Title One line, at most 255 characters\. GitLab: title must not mention prod/ },
      auto(/Request submitted/), { click: 'SIGNOUT' }]));
    common(r, 'job field');
  }
  // ---- client-side checks: the job's rules are applied before any commit ---------------------------------------------------------
  {
    const r = await run({ worldOpt: { noBlockers: true } }, toOptions.concat([
      { click: 'PREVIEW', form: { title: 'x'.repeat(300) } },
      { click: 'PREVIEW', form: { title: 'Short title', commitMessage: 'y'.repeat(2100) }, expect: /Fix 1 field: must be one line of at most 255 characters/ },
      { click: 'PREVIEW', form: { commitMessage: 'ok' }, expect: /Fix 1 field: must be at most 2000 characters/ },
      auto(/Preview: ready/), { click: 'SIGNOUT' }]));
    eq(commitsOf(r).length, 1, 'invalid options never reach GitLab');
    common(r, 'client checks');
    const r2 = await run({}, toOptions.concat([
      { click: 'PREVIEW' }, auto(/Predicted changes/), { click: 'EDIT' },
      { click: 'PREVIEW', form: { env: { qa: { base_url: GLTOKEN } } } },
      { click: 'SIGNOUT', expect: /This looks like a secret/ }]));
    eq(commitsOf(r2).length, 1, 'a secret-looking env value is refused before the commit');
    common(r2, 'secret value');
  }
  // ---- same in all environments (inlineValues) --------------------------------------------------------------------
  {
    const r = await run({}, toOptions.concat([
      { click: 'PREVIEW' }, auto(/Predicted changes/), { click: 'EDIT', expect: /qa ✕ 1 to fix/ },
      { click: 'PREVIEW', expect: /Same in all environments: use the source value for base_url/, form: Object.assign({ inline: { base_url: true } }, { env: { qa: { base_url: 'https://typed.example.com' } }, secrets: ENV_FORM.secrets }) },
      auto(/Preview: ready/),
      { click: 'SUBMIT', expect: /Same in all environments: base_url.*identical everywhere/ }, auto(/Request submitted/), { click: 'SIGNOUT' }]));
    const reqs = commitsOf(r).map(decodeReq);
    eq(reqs.map((x) => x.inlineValues), [undefined, ['base_url'], ['base_url']], 'inlineValues is sent once the value is ticked, and only then');
    ok(reqs.every((x) => !lib.has(x, 'envValues')), 'a value typed for an inlined variable is dropped: it is the same everywhere');
    const opt = r.pages.filter((p) => /Same in all environments: use the source value/.test(p.html))[0].html;
    ok((opt.match(/data-pp="\[&quot;inline&quot;,&quot;base_url&quot;\]"/g) || []).length === 3 && /identical everywhere/.test(opt), 'each environment row offers the option and says the value is identical everywhere');
    ok(r.pages.some((p) => /<h3>Same in all environments <span/.test(p.html)), 'after the preview the inlined variable stays listed');
    ok(r.w.g.jobRefusals.length === 0 && reqs[2].dryRun === false, 'the request job accepted the inline request');
    common(r, 'inline values');
    const r2 = await run({}, toOptions.concat([
      { click: 'PREVIEW' }, auto(/Predicted changes/), { click: 'EDIT' },
      { click: 'PREVIEW', form: { inline: { base_url: true } } }, auto(/Predicted changes/), { click: 'EDIT' },
      { click: 'PREVIEW', form: { inline: { base_url: false } }, expect: /Same in all environments: use the source value for base_url/ }, auto(/Predicted changes/), { click: 'SIGNOUT' }]));
    eq(commitsOf(r2).map(decodeReq).map((x) => x.inlineValues), [undefined, ['base_url'], undefined], 'unticking takes the variable out of inlineValues');
    common(r2, 'inline off');
  }
  // ---- _request blockers, warnings, one-at-a-time jobs -----------------------------------------------------------------------
  {
    const r = await run({ worldOpt: { noBlockers: true, requestBlocker: true, warning: true, jobStatuses: ['waiting_for_resource', 'waiting_for_resource', 'running', 'success'] } }, toOptions.concat([
      { click: 'PREVIEW' }, auto(/waiting for another request to finish/), auto(/Predicted changes/),
      { click: 'SIGNOUT', expect: /Whole request ✕ 1 to fix customer_secrets\.yml problem\. customer_secrets\.yml is not valid YAML/ }]));
    const pv = visible(r.pages[r.pages.length - 2].html);
    ok(/name does not match <id>/.test(pv), 'warnings are shown, with IDs redacted');
    common(r, 'request blockers');
  }
  // ---- applications and flow policies --------------------------------------------------------------------------------------
  {
    const r = await run({ worldOpt: { noBlockers: true } }, toOptions.concat([
      { click: 'EXTRAS', expect: /Applications and flow policies None\. Optional\./ },
      { click: 'SEARCH_APP', form: { appQuery: '' } },
      { click: 'SEARCH_APP', form: { appQuery: 'zzz' }, expect: /Type part of an application name/ },
      { click: 'SEARCH_APP', form: { appQuery: 'partner' }, expect: /No application in dev matches .zzz./ },
      { click: 'ADDAPP:0', expect: /2 matches\. Choose one; the app never picks for you.*Partner API.*Partner API/ },
      { click: 'SEARCH_APP', form: { appQuery: 'mobile' }, expect: /Two applications are named .Partner API.\. The request job selects by name/ },
      { click: 'SEARCH_APP', form: { appQuery: 'img' }, expect: /2 matches.*Mobile App.*Mobile App Beta/ },
      { click: 'SEARCH_APP', form: { appQuery: 'admin' }, expect: /1 match.*&lt;img src=x onerror=alert\(1\)&gt; app|1 match/ },
      { click: 'ADDAPP:0', expect: /1 match.*Admin Console/ },
      { click: 'SEARCH_APP', form: { appQuery: 'web' }, expect: /Applications \(1 of 20\).*Admin Console.*Remove application from the request/ },
      { click: 'POLICIES:0', expect: /1 match.*Web Portal.*Its flow policies/ },
      { click: 'SEARCH_POL', form: { polQuery: 'sign in' }, expect: /Flow policies of Web Portal.*Recovery policy.*Registration policy.*Sign in policy.*Sign in policy - MFA/ },
      { click: 'SEARCH_POL', form: { polQuery: 'registration' }, expect: /2 matches\. Choose one; the app never picks for you.*Sign in policy - MFA/ },
      { click: 'ADDPOL:0', expect: /1 match.*Registration policy/ },
      { click: 'SEARCH_APP', form: { appQuery: 'web' }, expect: /Flow policies \(1 of 20\).*Registration policy.*application Web Portal/ },
      { click: 'ADDAPP:0' }, { click: 'EXTRAS_DONE', expect: /Applications \(2 of 20\)/ },
      { click: 'PREVIEW', expect: /Application: Admin Console Application: Web Portal Flow policy: Registration policy \(application Web Portal\)/ },
      auto(/Preview: ready/), { click: 'SIGNOUT', expect: /Selected: .*Web Portal.*Registration policy/ }]));
    const reqs = commitsOf(r).map(decodeReq);
    eq(reqs[0].applications, [{ name: 'Admin Console' }, { name: 'Web Portal' }], 'applications are sent by name');
    eq(reqs[0].flowPolicies, [{ name: 'Registration policy' }], 'flow policies are sent by name');
    eq(reqs[0].flows[0].name, 'Registration', 'the main flow is still sent');
    ok(r.w.g.jobRefusals.length === 0, 'the request job accepted applications and flowPolicies');
    eq(glCalls(r, /api\.pingone\.com.*\/davinciApplications(\?cursor=[0-9]+)?$/).length, 3, 'the application list is read once (three pages) for every application search');
    eq(glCalls(r, /flowPolicies/).length, 2, 'flow policies: only the opened application is read (two pages)');
    ok(glCalls(r, /flowPolicies/).every((c) => c.url.indexOf('/davinciApplications/' + dvid(21) + '/flowPolicies') > 0), 'never one call per application');
    eq(glCalls(r, /as\/token/).length, 3, 'one worker token per list: flows, applications, flow policies');
    ok(r.w.calls.filter((c) => c.url.indexOf('api.pingone.com') > 0).every((c) => c.method === 'GET'), 'the lists are read-only');
    const budgets = r.budget.filter((b) => /SEARCH_APP|POLICIES|SEARCH_POL|ADD|EXTRAS|DROP/.test(b.button)).map((b) => b.button + '=' + b.calls);
    ok(budgets.indexOf('SEARCH_APP=4') >= 0 && budgets.indexOf('POLICIES:0=3') >= 0 && budgets.every((x) => /^(SEARCH_APP=(0|4)|POLICIES:0=3|[A-Z_:0-9]+=0)$/.test(x)), 'REST calls: 1 token + pages for a first search, 0 for the rest: ' + budgets.join(' '));
    ok(JSON.stringify(r.inst).indexOf('clientSecret') < 0 && JSON.stringify(r.pages.map((p) => p.state)).indexOf('secret-value-for-tests') < 0, 'application secrets from the list never reach the state');
    common(r, 'applications');
    const r2 = await run({ worldOpt: { noBlockers: true }, settings: { ppPathApplications: 'none' } }, toOptions.concat([{ click: 'SIGNOUT' }]));
    ok(!buttonOf(r2.pages[r2.pages.length - 2].html, 'EXTRAS') && !/Applications and flow policies/.test(r2.pages[r2.pages.length - 2].html), 'no application path: the step is not offered');
    common(r2, 'applications off');
    const r3 = await run({ settings: { ppPathFlowPolicies: '/v1/environments/{envId}/policies' } }, []);
    ok(/ppPathFlowPolicies: must be a \/v1\/\.\.\. path using .{1,6}envId.{1,6} and .{1,6}appId/.test(r3.last) && r3.last.indexOf(SRC) < 0, 'a bad path stops the app with its name, never a value');
    common(r3, 'applications bad path');
    const r4 = await run({ worldOpt: { noBlockers: true, manyApps: true, pageSize: 13 } }, toOptions.concat([{ click: 'EXTRAS' }, { click: 'SEARCH_APP', form: { appQuery: 'bulk' } }]
      .concat(Array.from({ length: 21 }, (_, i) => ({ click: 'ADDAPP:' + i })))
      .concat([{ click: 'EXTRAS_DONE', expect: /A request can hold at most 20 applications/ }, { click: 'PREVIEW', expect: /Applications and flow policies/ }, auto(/Preview: ready/), { click: 'SIGNOUT' }])));
    eq(commitsOf(r4).map(decodeReq)[0].applications.length, 20, 'at most 20 applications are sent');
    common(r4, 'applications limit');
  }
  // ---- artifact fallback: the download is a redirect (or 404); the job log's last line is used ------------------------------
  {
    const r = await run({ worldOpt: { noBlockers: true, artifactStatus: 302 } }, toOptions.concat([
      { click: 'PREVIEW' }, auto(/Preview: ready/),
      { click: 'SUBMIT', expect: /Summary only\. GitLab did not hand over result\.json.*full change list, the diff and the list of required secrets are not available here/ },
      auto(/Request submitted/),
      { click: 'WATCH', expect: /Open merge request.*Summary only.*1 new, 1 changed in source/ }, { click: 'SIGNOUT', expect: /Waiting for the merge request to be merged/ }]));
    eq(r.w.g.traces.length, 2, 'a redirect on result.json: the job log is read once per job');
    ok(r.w.g.traces.every((x) => x.range === contract.TRACE_RANGE), 'the log is requested with Range: bytes=-65536');
    ok(glCalls(r, /\/artifacts\/result\.json$/).length === 2 && glCalls(r, /\/jobs\/[0-9]+\/trace$/).length === 2, 'artifact first, then the trace');
    const pv = r.pages.filter((p) => /Preview: ready/.test(p.html))[0].html;
    ok(/Summary only/.test(pv) && !/<table/.test(pv.split('Predicted changes')[1].split('Readiness')[0]) && /1 new, 1 changed in source, 2 differs; git version kept/.test(visible(pv)), 'the preview shows the summary counts and no change table');
    ok(!/Review the change in GitLab/.test(pv), 'there is no diff link in log mode');
    const done = r.pages.filter((p) => /Request submitted/.test(p.html))[0].html;
    ok(/Open merge request/.test(done) && /Summary only/.test(done), 'a submit read from the log still links the merge request');
    eq(commitsOf(r).length, 2, 'the fallback writes nothing');
    common(r, 'artifact redirect');
    const r2 = await run({ worldOpt: { artifactStatus: 301, traceIgnoreRange: true } }, toOptions.concat([
      { click: 'PREVIEW' }, auto(/Summary only/), { click: 'SIGNOUT', expect: /qa ✕ 1 to fix.*stage ✕ 1 to fix.*prod ✕ 1 to fix/ }]));
    const blocked = r2.pages[r2.pages.length - 2].html;
    ok(buttonOf(blocked, 'SUBMIT').disabled && /Values are missing here/.test(blocked) && /Readiness: some environments are missing values/.test(visible(blocked)), 'blocked envs from the log: Readiness shows them and Submit stays off');
    common(r2, 'artifact redirect, whole log returned');
    const r3 = await run({ worldOpt: { noBlockers: true, noArtifact: true } }, toOptions.concat([
      { click: 'PREVIEW' }, auto(/Preview: ready/), { click: 'SIGNOUT', expect: /Summary only/ }]));
    eq(r3.w.g.traces.length, 1, 'a 404 on result.json also reads the job log');
    common(r3, 'artifact 404');
  }
  // ---- artifact fallback: a log without a result line ------------------------------------------------------------------------------
  {
    const r = await run({ worldOpt: { noBlockers: true, artifactStatus: 302, traceMode: 'none' } }, toOptions.concat([
      { click: 'PREVIEW' }, auto(/The preview did not finish/),
      { click: 'CHECK', expect: /GitLab answered the download with a redirect, HTTP 302\), and the job log has no result line\. Open the job in GitLab to see what happened\./ },
      auto(/The preview did not finish/), { click: 'SIGNOUT' }]));
    const pg = r.pages.filter((p) => /The preview did not finish/.test(p.html))[0].html;
    ok(buttonOf(pg, 'CHECK') && !/LOGNOISE|Job succeeded/.test(pg), 'no result line: a clear error and Check GitLab, never log text');
    ok(buttonOf(pg, 'PREVIEW') && !buttonOf(pg, 'PREVIEW').disabled, 'a failed preview can be run again');
    eq(commitsOf(r).length, 1, 'Check GitLab resumes waiting and does not commit');
    common(r, 'no result line, preview');
    const r2 = await run({ worldOpt: { noBlockers: true, artifactStatus: 404 } }, toOptions.concat([
      { click: 'PREVIEW' }, auto(/Preview: ready/),
      { do: (wd) => { wd.o.traceMode = 'none'; } },
      { click: 'SUBMIT' }, auto(/Outcome unknown/),
      { click: 'CHECK', expect: /GitLab has no result\.json for the job, HTTP 404\), and the job log has no result line.*Check GitLab/ },
      { click: 'SIGNOUT', expect: /Found in GitLab after Check GitLab/ }]));
    const unk = r2.pages.filter((p) => /Outcome unknown/.test(p.html))[0].html;
    ok(buttonOf(unk, 'CHECK') && buttonOf(unk, 'SUBMIT').disabled, 'a submit with no readable result is "outcome unknown", with Check GitLab');
    common(r2, 'no result line, submit');
    const r3 = await run({ worldOpt: { noBlockers: true, artifactStatus: 302, traceStatus: 502 } }, toOptions.concat([
      { click: 'PREVIEW' }, auto(/The preview did not finish/), { click: 'SIGNOUT', expect: /the job log could not be read \(HTTP 502\)/ }]));
    common(r3, 'no result line, log unreadable');
  }
  // ---- cancel on protected request branches -------------------------------------------------------------------------------------
  {
    const r = await run({ worldOpt: { noBlockers: true, protectedBranches: true } }, toOptions.concat([
      { click: 'PREVIEW' }, auto(/Preview: ready/), { click: 'CANCEL' }, { click: 'SIGNOUT', expect: /refused to delete .* \(HTTP 403\): request branches are protected here/ }]));
    eq(r.w.g.deletes.length, 0, 'nothing deleted');
    common(r, 'protected cancel');
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
