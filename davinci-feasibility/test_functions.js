'use strict';
// Node tests for the feasibility flow's custom functions (no tenant needed):
//   node test_functions.js [EXPORT.json]
// 1. Every function in the export is byte-identical to fn/*.js as gen_flow.py wraps it.
// 2. Unit tests for fn/_lib.js and fn/_translate.js.
// 3. The exported graph itself runs against a fake PingOne and a fake GitLab:
//    evaluators, saves, screens and functions, at every load level, with
//    refusals, failures and inputs fed both as objects and as JSON text.
// Test IDs are built at run time so no ID-shaped literal lives in the repo.
const assert = require('assert');
const fs = require('fs');
const path = require('path');

const HERE = __dirname;
const EXPORT = process.argv[2] || path.join(HERE, 'flow', 'pingone-promote-feasibility.flow.json');
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

let passed = 0;
const ok = (cond, msg) => { assert.ok(cond, msg); passed += 1; };
const eq = (a, b, msg) => { assert.deepStrictEqual(a, b, msg); passed += 1; };

// ---- 1. shipped code == sources --------------------------------------------
const read = (f) => fs.readFileSync(path.join(HERE, 'fn', f), 'utf8').replace(/\r\n/g, '\n');
const strip = (js) => js.split('\n').map((l) => l.trim()).filter((l) => l && !l.startsWith('//')).join('\n');
const LIB = strip(read('_lib.js'));
const TR = strip(read('_translate.js'));
const TRANSLATE_STEPS = ['inventory', 'fetch', 'assemble'];
const wrap = (step) => 'module.exports = a = async ({params}) => {\nconst lib = (() => {\n' + LIB + '\nreturn lib;\n})();\n' +
  (TRANSLATE_STEPS.indexOf(step) >= 0 ? 'const tr = (() => {\n' + TR + '\nreturn tr;\n})();\n' : '') + '// ---- step: ' + step + ' ----\n' + read(step + '.js') + '\n};\n';
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
const steps = fs.readdirSync(path.join(HERE, 'fn')).filter((f) => !f.startsWith('_') && f.endsWith('.js')).map((f) => f.slice(0, -3));
for (const s of steps) { ok(Object.keys(NODES).some((id) => NODES[id].capabilityName === 'customFunction' && NODES[id].properties.code.value.indexOf('// ---- step: ' + s + ' ----') >= 0), 'fn/' + s + '.js is used in the flow'); }
// eslint-disable-next-line no-new-func
const lib = new Function(LIB + '\nreturn lib;')();
// eslint-disable-next-line no-new-func
const tr = new Function(TR + '\nreturn tr;')();

// ---- test IDs (built, never written out) --------------------------------------
const hex = (n, len) => (n.toString(16) + '0'.repeat(len)).slice(0, len);
const uuid = (n) => hex(n + 0xa0, 8) + '-0000-4000-8000-' + hex(n + 0xb0, 12);
const dvid = (n) => 'd' + hex(n + 0xc0, 31);
const SRC = uuid(1);
const WORKER = uuid(2);
const PROJECT = '4242';
const BASIC = Buffer.from('client:secret-value-for-tests').toString('base64');
const GLTOKEN = 'glpat-' + 'T'.repeat(20);
const ACCESS = 'access-' + 'Z'.repeat(40);

// ---- 2. unit tests ---------------------------------------------------------------
eq(tr.key('PingOne SSO'), 'PingOne-20SSO', 'space is escaped as -20');
eq(tr.key('a-b'), 'a--b', 'dash is doubled');
eq(tr.key('é'), '-C3-A9', 'UTF-8 escaped byte by byte');
eq(tr.key('snake_case'), 'snake_case', 'underscore kept in resource keys');
eq(tr.variableKey('my_var', 'company', ''), 'my-5Fvar_company', 'underscore escaped in variable names');
const kc = tr.variableKey('attempts', 'company', '');
const kf = tr.variableKey('attempts', 'flow', 'Login');
ok(kc === 'attempts_company' && kf === 'attempts_flow_Login' && kc !== kf, 'company and flow variable with one name get distinct keys (terraformer bug avoided)');
ok(tr.variableKey('attempts', 'flow', 'Other') !== kf, 'one flow-variable name on two flows gets distinct keys');
ok(tr.variableKey('x', 'flowInstance', '') === 'x_flowInstance' && tr.variableKey('x', 'user', '') === 'x_user', 'flowInstance and user contexts');
assert.throws(() => tr.variableKey('x', 'flow', ''), /flow/); passed += 1;
assert.throws(() => tr.variableKey('x', 'global', ''), /context/); passed += 1;
const TFVAR = /^[A-Za-z][A-Za-z0-9_]*$/;
for (const k of [tr.variableSecretKey('api-Token_company'), tr.secretKey('connection', 'PingOne-20SSO', 'clientSecret'), tr.secretKey('flow', 'Login_ab12cd34ef', 'apiKey')]) {
  ok(TFVAR.test(k), 'secret key is a Terraform variable name: ' + k);
}
eq(tr.variableSecretKey('apiToken_company'), 'davinci_variable_apiToken_company_value', 'secret variable key format');
eq(tr.q('a${b}%{c}"\\\n'), '"a$${b}%%{c}\\"\\\\\\n"', 'HCL string escaping (${ and %{ doubled)');
eq(tr.canon({ b: 1, a: [true, null, 'x'] }), '{\n  "a": [\n    true,\n    null,\n    "x"\n  ],\n  "b": 1\n}', 'canonical JSON sorts keys');
eq(tr.render([['name', tr.lit('x')], ['data_type', tr.lit('number')], ['value', { block: [['float32', tr.lit(3)]] }]], 1, false),
  '  name      = "x"\n  data_type = "number"\n  value = {\n    float32 = 3\n  }', 'attribute alignment like terraform fmt');
eq(tr.render([['properties', { json: { m: '${x}' } }]], 0, false), 'properties = <<-JSON\n  {\n    "m": "$${x}"\n  }\nJSON', 'heredoc escapes ${');
const deep = { a: { calls: ['b'] }, b: { calls: ['c'] }, c: { calls: ['d'] }, d: { calls: ['e'] }, e: { calls: [] } };
ok(tr.assignTiers(deep).problems.length === 1, 'nesting deeper than four tiers is reported');
const t3 = tr.assignTiers({ top: { calls: ['mid', 'leaf'] }, mid: { calls: ['leaf'] }, leaf: { calls: [] }, ext: { calls: ['nowhere'] } });
eq([t3.tiers.top, t3.tiers.mid, t3.tiers.leaf, t3.tiers.ext], [3, 2, 1, 3], 'tiers: callers above callees (longest path)');
eq(t3.external, ['nowhere'], 'subflows outside the snippet are listed');
const sctx = { ids: { [dvid(9)]: '@@flow:x@@' }, secretKind: 'flow', secretOwner: 'f_n', secrets: {}, stats: tr.newStats() };
const scrubbed = tr.scrub({ clientSecret: { value: 'literal' }, accessToken: { value: '{{local.x.payload.output.t}}' }, other: { value: '******' },
  sec: { value: 'v', secure: true }, ref: { value: 'see ' + dvid(9) + ' and @@ and ' + dvid(10) } }, sctx);
ok(scrubbed.clientSecret.value === '@@secret:davinci_flow_f_n_clientSecret@@' && scrubbed.other.value.indexOf('@@secret:') === 0 && scrubbed.sec.value.indexOf('@@secret:') === 0,
  'secret-named literals, masked values and secure properties become secret placeholders');
ok(scrubbed.accessToken.value.indexOf('{{local') === 0, 'a {{...}} expression is not treated as a secret');
ok(scrubbed.ref.value === 'see @@flow:x@@ and @@@@ and ' + dvid(10) && sctx.stats.unresolvedIds === 1, 'known IDs become placeholders, @@ is escaped, unknown IDs are counted');
ok(!/literal|"v"/.test(JSON.stringify(scrubbed)), 'secret values are never emitted');

const red = lib.redact('Bearer ' + ACCESS + ' ' + GLTOKEN + ' env ' + SRC + ' id ' + dvid(1) + ' mail a.b@example.com at https://x.example/y');
ok(red.indexOf(ACCESS) < 0 && red.indexOf(GLTOKEN) < 0 && red.indexOf(SRC) < 0 && red.indexOf(dvid(1)) < 0 && red.indexOf('example.com') < 0 && red.indexOf('https') < 0, 'redact removes tokens, IDs, emails and URLs');
ok(lib.redact('x'.repeat(500)).length <= 160, 'redact cuts long text');
for (const b of ['main', 'Master', 'production', 'release/1.2', 'hotfix/x', 'bad..name', 'trailing/', 'x.lock', '-lead', 'a//b', 'HEAD']) { ok(lib.checkBranch(b) !== '', 'branch refused: ' + b); }
for (const b of ['davinci-feasibility/run-1', 'feasibility_test.2']) { ok(lib.checkBranch(b) === '', 'branch allowed: ' + b); }
ok(lib.matchesPattern('release/*', 'release/9') && lib.matchesPattern('feat-*', 'feat-x') && !lib.matchesPattern('main', 'mainline'), 'protected-branch wildcard matching');
assert.throws(() => lib.fillPath('/v1/environments/{envId}/../x', { envId: 'e' })); passed += 1;
assert.throws(() => lib.fillPath('https://evil.example/v1/x', {})); passed += 1;
assert.throws(() => lib.fillPath('/v1/environments/{other}', { envId: 'e' })); passed += 1;
assert.throws(() => lib.fillPath('/v1/environments/{envId}', { envId: '../x' })); passed += 1;
eq(lib.fillPath('/v1/environments/{envId}/flows/{flowId}', { envId: 'e1', flowId: 'f1' }), '/v1/environments/e1/flows/f1', 'path template filled');
eq([lib.utf8Len('aé€😀'), lib.size({ a: 1 })], [10, 7], 'UTF-8 byte length');
eq(lib.parse('{"a":1}'), { a: 1 }, 'JSON text inputs are parsed');
eq(lib.str('{{global.variables.x}}'), '', 'an unresolved reference counts as unset');

// ---- 3. run the exported graph ------------------------------------------------------
// Fake tenant: three linked flows, filler flows, two connections, variables.
const CONN_HTTP = dvid(100);
const CONN_SSO = dvid(101);
function makeFlows(count) {
  const ids = { 'Error leaf': dvid(1), Login: dvid(2), 'MFA sub': dvid(3) };
  const node = (id, conn, cap, props) => ({ data: { id: id, nodeType: 'CONNECTION', connectionId: conn, connectorId: 'httpConnector', capabilityName: cap, label: 'Http', status: 'configured', type: 'trigger', properties: props }, position: { x: 10, y: 20.5 } });
  const sub = (id, target, label) => node(id, CONN_HTTP, 'startUiSubFlow', { subFlowId: { value: { label: label, value: ids[target] } }, subFlowVersionId: { value: -1 } });
  const out = [
    { id: ids['Error leaf'], name: 'Error leaf', enabled: true, graphData: { elements: { nodes: [node('leaf000001', CONN_HTTP, 'customHtmlMessage', { message: { value: 'Hi ${user.name}, 100%{done} @@ literal' } })], edges: [] } } },
    { id: ids.Login, name: 'Login', description: 'Primary sign-on', color: '#AFD5FF', enabled: true, trigger: { type: 'AUTHENTICATION' },
      settings: { csp: "script-src 'self'", logLevel: 2, cssLinks: ['https://cdn.example.com/b.css', 'https://cdn.example.com/a.css'], unknownSetting: { x: 1 } },
      graphData: { elements: { nodes: [
        node('login00001', CONN_SSO, 'userLookup', { envId: { value: SRC }, clientSecret: { value: 'literal-secret-xyz' }, apiKey: { value: '******' }, theme: { value: uuid(77) } }),
        sub('login00002', 'MFA sub', 'MFA sub'),
        { data: { id: 'login00003', nodeType: 'EVAL', label: 'Evaluator' }, position: { x: 1, y: 2 } }
      ], edges: [{ data: { id: 'le1', source: 'login00001', target: 'login00003' } }, { data: { id: 'le2', source: 'login00003', target: 'login00002', multiValueSourceId: 'found' } }] } } },
    { id: ids['MFA sub'], name: 'MFA sub', enabled: false, inputSchema: [{ preferredDataType: 'string', propertyName: 'username', required: true, preferredControlType: 'textField' }],
      outputSchema: { output: { type: 'object', properties: { result: { type: 'string' } }, additionalProperties: false } },
      graphData: { elements: { nodes: [sub('mfa0000001', 'Error leaf', 'Error leaf'), node('mfa0000002', CONN_HTTP, 'customHtmlMessage', { variableId: { value: uuid(50) } })], edges: [] } } }
  ];
  for (let i = 1; i <= count; i++) {
    out.push({ id: dvid(10 + i), name: 'Pad ' + String(i).padStart(2, '0'), enabled: true,
      graphData: { elements: { nodes: [node('pad' + String(i).padStart(7, '0'), CONN_HTTP, 'customHtmlMessage', { message: { value: 'x'.repeat(2000) } })], edges: [] } } });
  }
  return out;
}
const CONNECTIONS = [
  { id: CONN_HTTP, name: 'Http', connector: { id: 'httpConnector' } },
  { id: CONN_SSO, name: 'PingOne SSO', connector: { id: 'pingOneSSOConnector' }, properties: { clientId: { type: 'string', value: 'client-id-literal' }, clientSecret: { type: 'string', value: '******', secure: true }, envId: { type: 'string', value: SRC } } },
  { id: dvid(102), name: 'Unused', connector: { id: 'genericConnector' } }
];
const VARIABLES = [
  { id: uuid(50), name: 'attempts', context: 'company', dataType: 'number', mutable: true, min: 0, max: 10, value: 3 },
  { id: uuid(51), name: 'attempts', context: 'flow', dataType: 'number', mutable: true, value: 0, flow: { id: dvid(2) } },
  { id: uuid(52), name: 'apiToken', context: 'company', dataType: 'secret', mutable: false, value: '******' },
  { id: uuid(53), name: 'greeting', context: 'flowInstance', dataType: 'string', mutable: true, value: 'Hello ${user.name}' },
  { id: uuid(54), name: 'theme', context: 'company', dataType: 'object', mutable: true, value: { dark: false } },
  { id: uuid(55), name: 'counter', context: 'flow', dataType: 'number', mutable: true, value: 1, flow: { id: dvid(30) } } // Pad 20: not in the level-5 set
];

function world(opt) {
  const o = Object.assign({ pads: 25, pageSize: 10, visibility: 'private', defaultBranch: 'main', branchExists: false, protectedList: [{ name: 'main' }],
    protectedStatus: 200, fileExists: false, tokenStatus: 200, failFlow: null, shuffle: false, mrStatus: 201 }, opt || {});
  const flows = makeFlows(o.pads);
  const order = (a) => (o.shuffle ? a.slice().reverse() : a);
  const calls = [];
  const api = 'https://api.pingone.com/v1/environments/' + SRC;
  const gl = 'https://gitlab.example.com/api/v4/projects/' + PROJECT;
  const page = (items, key, base, q, pg) => {
    const start = pg * o.pageSize;
    const body = { _embedded: { [key]: items.slice(start, start + o.pageSize) }, count: items.length, size: o.pageSize };
    if (start + o.pageSize < items.length) { body._links = { next: { href: base + '?' + (q ? q + '&' : '') + 'cursor=' + (pg + 1) } }; }
    return { status: 200, body: body };
  };
  const handler = (method, url, headers, body) => {
    calls.push({ method: method, url: url, headers: headers, body: body });
    const pg = Number((/cursor=([0-9]+)/.exec(url) || [0, 0])[1]);
    const bare = url.split('?')[0];
    if (method === 'POST' && url === 'https://auth.pingone.com/' + WORKER + '/as/token') {
      assert.strictEqual(headers.Authorization, 'Basic ' + BASIC);
      assert.strictEqual(body, 'grant_type=client_credentials');
      return o.tokenStatus === 200 ? { status: 200, body: { access_token: ACCESS, token_type: 'Bearer', expires_in: 3600 } }
        : { status: o.tokenStatus, body: { code: 'invalid_client', message: 'client ' + WORKER + ' Bearer ' + ACCESS + ' rejected' } };
    }
    if (url.indexOf('https://api.pingone.com/') === 0) {
      assert.strictEqual(method, 'GET', 'PingOne is read-only');
      assert.strictEqual(headers.Authorization, 'Bearer ' + ACCESS);
      if (bare === api + '/flows') {
        assert.ok(url.indexOf('attributes=id,name') > 0, 'flows list asks for id,name only');
        return page(order(flows.map((f) => ({ id: f.id, name: f.name }))), 'flows', api + '/flows', 'attributes=id,name', pg);
      }
      if (bare === api + '/connectorInstances') { return page(order(CONNECTIONS), 'connectorInstances', api + '/connectorInstances', '', pg); }
      if (bare === api + '/variables') { return page(order(VARIABLES), 'variables', api + '/variables', '', pg); }
      const m = /\/flows\/([0-9a-f]{32})$/.exec(bare);
      if (m) {
        if (o.failFlow === m[1]) { return { status: 500, body: { message: 'boom' } }; }
        const f = flows.filter((x) => x.id === m[1])[0];
        return f ? { status: 200, body: f } : { status: 404, body: {} };
      }
      return { status: 404, body: {} };
    }
    if (url.indexOf(gl) === 0) {
      assert.strictEqual(headers['PRIVATE-TOKEN'], GLTOKEN);
      const rest = url.slice(gl.length);
      if (method === 'GET' && rest === '') { return { status: 200, body: { id: 4242, visibility: o.visibility, archived: false, default_branch: o.defaultBranch } }; }
      if (method === 'GET' && rest.indexOf('/repository/branches/') === 0) { return o.branchExists ? { status: 200, body: { name: 'x' } } : { status: 404, body: { message: '404 Branch Not Found' } }; }
      if (method === 'GET' && rest.indexOf('/protected_branches') === 0) { return { status: o.protectedStatus, body: o.protectedStatus === 200 ? o.protectedList : { message: '403 Forbidden' } }; }
      if (method === 'GET' && rest.indexOf('/repository/files/') === 0) { return o.fileExists ? { status: 200, body: { file_name: 'x' } } : { status: 404, body: { message: '404 File Not Found' } }; }
      if (method === 'POST' && rest === '/repository/commits') { return { status: 201, body: { id: 'abc', short_id: 'abc' } }; }
      if (method === 'POST' && rest === '/merge_requests') { return { status: o.mrStatus, body: { iid: 7, web_url: 'https://gitlab.example.com/g/p/-/merge_requests/7' } }; }
    }
    throw new Error('unexpected call ' + method + ' ' + url);
  };
  return { handler: handler, calls: calls, flows: flows };
}

const SETTINGS = { fsApiOrigin: 'https://api.pingone.com', fsAuthOrigin: 'https://auth.pingone.com', fsWorkerEnvironmentId: WORKER, fsSourceEnvironmentId: SRC,
  fsGitlabApi: 'https://gitlab.example.com/api/v4', fsGitlabProjectId: PROJECT };

async function run(opt) {
  const o = Object.assign({ level: 'N5', branch: '', openMr: '', handBack: '', target: 'dev', strings: false, settings: {} }, opt);
  const w = world(o.world);
  const flowVars = {};
  for (const v of flow.variables) { if (v.context === 'flow') { flowVars[v.name.split('##SK##')[0]] = v.fields.value; } }
  Object.assign(flowVars, SETTINGS, o.settings);
  const company = { fsWorkerBasicAuth: BASIC, fsGitlabToken: GLTOKEN };
  const inst = {};
  const outputs = {};
  const pages = [];
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
  let restCalls = 0;
  for (let stepN = 0; stepN < 50000; stepN++) {
    const d = NODES[id];
    const p = d.properties || {};
    let result = true;
    if (d.nodeType === 'EVAL') { throw new Error('evaluator reached directly'); }
    switch (d.capabilityName) {
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
        restCalls += 1;
        outputs[id] = { rawResponse: { statusCode: res.status, body: res.body } };
        result = res.status >= 200 && res.status <= 299;
        break;
      }
      case 'customHTMLTemplate': {
        const html = template(p.customHTML.value);
        pages.push({ title: title(id), html: html });
        if (!SUCC[id]) { return { w: w, pages: pages, inst: inst, outputs: outputs, result: html, restCalls: restCalls }; }
        const fields = p.formFieldsList.value.map((x) => x.propertyName);
        const answer = title(id) === 'Start page'
          ? { buttonValue: o.level, targetName: o.target, branchName: o.branch, openMr: o.openMr, handBackEvery: o.handBack }
          : { buttonValue: 'CONTINUE' };
        outputs[id] = {};
        for (const f of fields) { outputs[id][f] = answer[f] === undefined ? '' : answer[f]; }
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

const SECRETS = [BASIC, GLTOKEN, ACCESS, 'secret-value-for-tests', 'literal-secret-xyz'];
const IDS = [SRC, WORKER, CONN_HTTP, CONN_SSO, dvid(1), dvid(2)];
function resultsOf(r) {
  const fin = Object.keys(NODES).filter((x) => title(x) === '6 Results')[0];
  const txt = r.outputs[fin].resultsText.replace(/&quot;/g, '"').replace(/&#39;/g, "'").replace(/&lt;/g, '<').replace(/&gt;/g, '>').replace(/&amp;/g, '&');
  return JSON.parse(txt);
}
function noLeaks(r, label) {
  const shown = r.pages.map((p) => p.html).join('\n');
  const res = JSON.stringify(resultsOf(r));
  for (const s of SECRETS) { ok(shown.indexOf(s) < 0 && res.indexOf(s) < 0, label + ': no secret on any screen or in the results'); }
  for (const s of IDS) { ok(res.indexOf(s) < 0, label + ': no tenant ID in the copied results'); }
  ok(JSON.stringify(r.inst.fsState || {}).indexOf(ACCESS) < 0, label + ': the token never enters the run state');
}
const commitOf = (r) => { const c = r.w.calls.filter((x) => x.url.indexOf('/repository/commits') > 0)[0]; return c && JSON.parse(c.body); };
const check = (res, id) => (res.checks.filter((c) => c.id === id)[0] || {}).result;

async function main() {
  // Settings page reports missing values by name only.
  const missing = await run({ settings: { fsSourceEnvironmentId: '' } });
  ok(missing.pages[0].html.indexOf('fsSourceEnvironmentId') > 0 && missing.pages[0].html.indexOf(WORKER) < 0, 'settings check names the missing variable, shows no values');
  ok(missing.w.calls.length === 0 && resultsOf(missing).verdict === 'FAIL', 'missing settings: no calls, FAIL');

  // Level 1, no GitLab.
  const one = await run({ level: 'N1' });
  const r1 = resultsOf(one);
  ok(r1.fetch.ok === 1 && r1.counts.flowsInEnv === 28 && r1.counts.connectorInstances === 3 && r1.counts.variables === 6, 'level 1: one flow fetched, inventory counted across pages');
  ok(one.w.calls.filter((c) => c.url.indexOf('/flows?') > 0).length === 3, 'flows list paged (3 pages of 10)');
  ok(check(r1, 'gitlab_write') === 'SKIP' && r1.gitlab === null && !one.w.calls.some((c) => c.url.indexOf('gitlab') > 0), 'no branch: GitLab skipped');
  ok(r1.verdict !== 'FAIL', 'level 1 does not fail: ' + JSON.stringify(r1.checks.filter((c) => c.result !== 'PASS')));
  ok(r1.calibration.loops === 10 && r1.stages.length === 6 && r1.stages.every((s) => typeof s.status === 'string'), 'calibration ran; six stages reported');
  noLeaks(one, 'level 1');

  // Level 5 with a branch: the commit.
  const five = await run({ level: 'N5', branch: 'davinci-feasibility/run-1' });
  const r5 = resultsOf(five);
  const c5 = commitOf(five);
  ok(r5.fetch.ok === 5 && check(r5, 'coverage') === 'PASS', 'level 5: five flows fetched and translated');
  eq([c5.branch, c5.start_branch, c5.actions.length, c5.actions[0].action, c5.actions[0].file_path], ['davinci-feasibility/run-1', 'main', 1, 'create', 'environments/dev.tfvars'],
    'commit goes to the new branch from the default branch, one create action');
  ok(!c5.force && !five.w.calls.some((c) => c.url.indexOf('/merge_requests') > 0), 'no force, no merge request by default');
  const tf = c5.actions[0].content;
  // FS_DUMP=<dir>: write the level-5 snippet, results JSON and result screen for a look.
  if (process.env.FS_DUMP) {
    fs.writeFileSync(path.join(process.env.FS_DUMP, 'sample.tfvars'), tf);
    fs.writeFileSync(path.join(process.env.FS_DUMP, 'sample-results.json'), JSON.stringify(r5, null, 2));
    fs.writeFileSync(path.join(process.env.FS_DUMP, 'sample-result.html'), five.result);
  }
  for (const want of ['tfvars_schema_version  = 1', '"attempts_company" = {', '"attempts_flow_Login" = {', 'flow      = "Login"', '"@@flow:MFA-20sub@@"', '"@@flow:Error-20leaf@@"',
    'connection      = "PingOne-20SSO"', '"@@env:id@@"', '"@@variable:attempts_company@@"', '@@secret:davinci_flow_Login_login00001_clientSecret@@',
    '@@secret:davinci_flow_Login_login00001_apiKey@@', '@@secret:davinci_connection_PingOne-20SSO_clientSecret@@'.replace('-', '_'),
    'secret    = "davinci_variable_apiToken_company_value"', '$${user.name}', '%%{done}', '@@@@ literal', '"Hello $${user.name}"', 'json_object = <<-JSON',
    'css_links = ["https://cdn.example.com/a.css", "https://cdn.example.com/b.css"]', 'multi_value_source_id = "found"', 'trigger = {']) {
    ok(tf.indexOf(want) >= 0, 'tfvars contains ' + want);
  }
  const tierOf = (k) => Number((new RegExp('"' + k + '" = \\{\\n    name[^\\n]*\\n(?:    [a-z_]+ += [^\\n]*\\n)*?    tier += ([0-3])').exec(tf) || [0, -1])[1]);
  eq([tierOf('Login'), tierOf('MFA-20sub'), tierOf('Error-20leaf'), tierOf('Pad-2001')], [3, 2, 1, 3], 'subflow tiers in the snippet');
  for (const bad of ['******', 'literal-secret-xyz', CONN_HTTP, CONN_SSO, dvid(1), dvid(3), SRC, '"Unused"', 'counter']) { ok(tf.indexOf(bad) < 0, 'tfvars never contains ' + bad.slice(0, 20)); }
  ok(tf.indexOf(uuid(77)) > 0 && r5.tfvars.unresolvedIds === 1 && check(r5, 'translation') === 'WARN', 'an unknown ID is kept and reported as unresolved');
  ok(/"Login" = \{\n    name        = "Login"\n    description = "Primary sign-on"\n    color       = "#AFD5FF"\n    tier        = 3\n/.test(tf), 'flow attributes aligned in schema order');
  ok(five.pages[five.pages.length - 1].html.indexOf('TF_VAR_davinci_variable_apiToken_company_value: apps/pingone-davinci/${VAULT_PATH_ENV}/pingone/davinci_variable_apiToken_company_value@secret') > 0,
    'result screen lists the customer_secrets.yml TF_VAR entries');
  eq(five.inst.fsState.secretKeys, ['davinci_connection_PingOne_20SSO_clientSecret', 'davinci_flow_Login_login00001_apiKey',
    'davinci_flow_Login_login00001_clientSecret', 'davinci_variable_apiToken_company_value'], 'exactly the expected secret keys');
  ok(r5.secretsNeeded === 4 && r5.gitlab.committed && !r5.gitlab.mrOpened, 'results: secrets counted, committed, no MR');
  ok(r5.stages.filter((s) => s.stage === 'gitlab')[0].calls === 5, 'GitLab stage: project, branch, protected, file, commit');
  noLeaks(five, 'level 5');

  // Determinism: other listing order and inputs as JSON text give the same file.
  const again = await run({ level: 'N5', branch: 'davinci-feasibility/run-1', strings: true, world: { shuffle: true } });
  eq(commitOf(again).actions[0].content, tf, 'tfvars is deterministic (listing order, object or text inputs)');

  // All flows, hand-backs every 3 calls, merge request on, file exists.
  const all = await run({ level: 'ALL', branch: 'davinci-feasibility/all', openMr: 'yes', handBack: '3', world: { fileExists: true } });
  const ra = resultsOf(all);
  ok(ra.fetch.ok === 28 && ra.totals.handbacks >= 10 && all.pages.filter((p) => p.title.indexOf('Working') === 0).length === ra.totals.handbacks, 'all flows with hand-backs to the browser');
  ok(ra.gitlab.mrOpened && commitOf(all).actions[0].action === 'update', 'MR opened when asked; existing file updated on the new branch');
  const mr = JSON.parse(all.w.calls.filter((c) => c.url.indexOf('/merge_requests') > 0)[0].body);
  ok(mr.source_branch === 'davinci-feasibility/all' && mr.target_branch === 'main' && /^Draft:/.test(mr.title), 'merge request from the new branch, marked draft');
  ok(all.pages[all.pages.length - 1].html.indexOf('merge_requests/7') > 0 && JSON.stringify(ra).indexOf('merge_requests') < 0, 'MR link on screen, not in the copied results');
  noLeaks(all, 'all flows');
  const never = await run({ level: 'ALL', handBack: '0' });
  ok(resultsOf(never).totals.handbacks === 0 && resultsOf(never).totals.segments === 1, 'hand-back 0: one browser request');

  // Refusals: nothing is written.
  for (const [label, o, where] of [
    ['default-looking name', { branch: 'main' }, 'start'], ['protected prefix', { branch: 'release/1.0' }, 'start'],
    ['branch exists', { branch: 'davinci-feasibility/x', world: { branchExists: true } }, 'gitlab'],
    ['protected pattern', { branch: 'feat/x', world: { protectedList: [{ name: 'feat/*' }] } }, 'gitlab'],
    ['public project', { branch: 'davinci-feasibility/x', world: { visibility: 'public' } }, 'gitlab'],
    ['default branch named', { branch: 'trial-x', world: { defaultBranch: 'trial-x' } }, 'gitlab']]) {
    const r = await run(Object.assign({ level: 'N1' }, o));
    const res = resultsOf(r);
    ok(!r.w.calls.some((c) => c.method === 'POST' && c.url.indexOf('gitlab') > 0), 'refused (' + label + '): no GitLab write');
    if (where === 'start') { ok(r.w.calls.length === 0 && res.verdict === 'FAIL', 'refused at start (' + label + ')'); } else { ok(res.stages.filter((s) => s.stage === 'gitlab')[0].status === 'refused' && res.verdict !== 'PASS', 'refused by GitLab check (' + label + ')'); }
  }
  const p403 = await run({ level: 'N1', branch: 'davinci-feasibility/y', world: { protectedStatus: 403 } });
  ok(resultsOf(p403).gitlab.committed && resultsOf(p403).notes.some((n) => /protected branches/.test(n)), 'protected list unreadable: name checks only, noted');

  // Failures.
  const bad = await run({ level: 'N5', world: { tokenStatus: 401 } });
  const rb = resultsOf(bad);
  ok(bad.w.calls.length === 1 && rb.verdict === 'FAIL' && rb.errors[0].status === 401 && rb.errors[0].code === 'invalid_client', 'token failure: stops, FAIL, status and code kept');
  ok(JSON.stringify(rb.errors).indexOf(WORKER) < 0 && JSON.stringify(rb.errors).indexOf(ACCESS) < 0, 'upstream error text redacted');
  noLeaks(bad, 'token failure');
  const flaky = await run({ level: 'N5', world: { failFlow: dvid(3) } });
  const rf = resultsOf(flaky);
  ok(rf.fetch.ok === 4 && rf.fetch.failed === 1 && check(rf, 'coverage') === 'FAIL' && rf.verdict === 'FAIL', 'one flow failing: run continues, coverage FAIL');
  const late = await run({ level: 'N5', branch: 'davinci-feasibility/z', openMr: 'yes', world: { mrStatus: 409 } });
  ok(resultsOf(late).gitlab.committed && !resultsOf(late).gitlab.mrOpened && resultsOf(late).verdict === 'FAIL', 'MR failure reported after the commit');

  console.log('all ' + passed + ' checks passed');
}
main().catch((e) => { console.error(e.stack || e.message); process.exit(1); });
