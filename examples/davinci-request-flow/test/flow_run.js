// Runs the generated DaVinci export itself, node by node, against a fake
// PingOne and a fake GitLab. Covers what the step functions alone cannot:
// evaluator branches, node references, flow variables, page templates and
// the exact HTTP requests each REST node sends.
//
// usage: node test/flow_run.js out/pingone-promote-request.flow.json [DIR]
// With DIR, every committed request.json is written there for the Go check.
const assert = require('assert');
const fs = require('fs');
const path = require('path');

const FLOW = JSON.parse(fs.readFileSync(process.argv[2], 'utf8'));
const SAVE_DIR = process.argv[3] || '';
const O = 'https://api.pingone.com';
const A = 'https://auth.pingone.com';
const G = 'https://gitlab.example.com/tools';
const ROLE = 'role-dva';
// PingOne environment IDs are UUIDs (the settings check enforces it).
const SRC = '11111111-1111-4111-8111-111111111111';
const TGT = '22222222-2222-4222-8222-222222222222';
const HOST = '33333333-3333-4333-8333-333333333333';
const ADMINS = '44444444-4444-4444-8444-444444444444';

// ---------------- the interpreter ----------------
// The import file holds the main flow and its subflows ({"flows": [...]}).
// Node IDs are unique across the file, so one table serves every flow.
const FLOWS = FLOW.flows || [FLOW];
const BY_ID = {};
const nodes = {};
const out = {};
const START_OF = {};
for (const fl of FLOWS) {
  BY_ID[fl.flowId] = fl;
  for (const n of fl.graphData.elements.nodes) { assert.ok(!(n.data.id in nodes), 'node id repeats across flows: ' + n.data.id); nodes[n.data.id] = n.data; }
  for (const e of fl.graphData.elements.edges) { (out[e.data.source] = out[e.data.source] || []).push(e.data.target); }
  const incoming = new Set(fl.graphData.elements.edges.map((e) => e.data.target));
  const starts = fl.graphData.elements.nodes.map((n) => n.data.id).filter((id) => !incoming.has(id));
  assert.strictEqual(starts.length, 1, fl.name + ': one start node');
  START_OF[fl.flowId] = starts[0];
}
const MAIN = FLOWS[0];
// Sign-in is optional (off for the proof of concept); the checks follow the export.
const SIGNIN = Object.values(nodes).some((n) => n.capabilityName === 'loginFirstFactor');

function get(obj, dotted) {
  return dotted.split('.').reduce((v, k) => (v === undefined || v === null ? undefined : v[k]), obj);
}

// One flow instance: its own flow variables, node outputs and (for a subflow) input parameters.
function makeRun(flow, settings, secrets, params, stringMode) {
  // Flow variables come from the import; the settings stand in for the values the admin sets in DaVinci (main flow only).
  const flowVars = {};
  for (const v of flow.variables) {
    if (v.context === 'flow') { flowVars[v.fields.displayName] = v.fields.value; }
  }
  if (flow === MAIN) { Object.assign(flowVars, settings); }
  const outputs = {};
  const lookup = (ref) => {
    let m;
    if ((m = /^local\.([a-z0-9]+)\.payload\.output\.(.+)$/.exec(ref))) {
      assert.ok(m[1] in outputs, 'reference to a node that has not run (in ' + flow.name + '): ' + ref);
      return get(outputs[m[1]], m[2]);
    }
    if ((m = /^global\.flow\.variables\.(.+)$/.exec(ref))) { assert.ok(m[1] in flowVars, 'flow variable read before any node set it: ' + m[1]); return flowVars[m[1]]; }
    if ((m = /^global\.company\.variables\.(.+)$/.exec(ref))) { return secrets[m[1]]; }
    if ((m = /^global\.parameters\.(.+)$/.exec(ref))) { assert.ok(m[1] in params, flow.name + ' reads parameter it was not given: ' + m[1]); return params[m[1]]; }
    throw new Error('unknown reference ' + ref);
  };
  const asText = (v) => (v === undefined || v === null ? '' : typeof v === 'object' ? JSON.stringify(v) : String(v));
  // A slate value: one link alone keeps its type; anything else is text.
  const slate = (raw, keepType) => {
    const parts = JSON.parse(raw)[0].children.filter((c) => !(c.text === '' && !c.type));
    if (keepType && parts.length === 1 && parts[0].type === 'link') {
      const v = lookup(parts[0].data.slice(2, -2));
      return stringMode && typeof v === 'object' && v !== null ? JSON.stringify(v) : v;
    }
    return parts.map((c) => (c.type === 'link' ? asText(lookup(c.data.slice(2, -2))) : c.text)).join('');
  };
  const template = (html) => html.replace(/\{\{([^}]+)\}\}/g, (m, ref) => asText(lookup(ref)));
  return { flow, flowVars, outputs, slate, template };
}

async function runFlow(world, form, opts) {
  opts = opts || {};
  // Shared by the main flow and every subflow it invokes: the screens shown and
  // the REST calls made within one browser request (between two rendered pages).
  const st = { pages: [], calls: 0, maxCalls: 0, subflows: [] };
  const main = makeRun(MAIN, world.settings, world.secrets, {}, !!opts.stringMode);
  const res = await runGraph(MAIN, main, world, form, opts, st);
  main.maxCalls = st.maxCalls;
  res.pages = st.pages;
  res.run = main;
  res.subflows = st.subflows;
  return res;
}

// Runs one flow until it ends: { end: 'page' | 'error' | 'message', ... } stops everything;
// { end: 'success', claims } returns a subflow's values to its Flow Conductor node.
async function runGraph(flow, r, world, form, opts, st) {
  let id = START_OF[flow.flowId];
  for (let steps = 0; steps < 5000; steps++) {
    const n = nodes[id];
    const p = n.properties || {};
    const title = p.nodeTitle.value;
    let result = true;
    switch (n.capabilityName) {
      case 'loginFirstFactor':
        r.outputs[id] = { accessToken: 'at-' + world.user };
        break;
      case 'makeRestApiCall': {
        st.calls++;
        st.maxCalls = Math.max(st.maxCalls, st.calls);
        const headers = {};
        for (const h of p.headers.value) { headers[h.key] = r.slate(h.value); }
        const body = p.httpBody && p.httpBody.value === 'raw' ? r.template(p.raw.value) : undefined;
        const res = world.http(p.httpMethod.value, r.slate(p.url.value), headers, body);
        // DaVinci treats a non-2xx response as a failed step (its false branch).
        // world.dropFailedOutput models a failed step that keeps no output.
        result = res.status < 400;
        r.outputs[id] = !result && world.dropFailedOutput ? {} : { rawResponse: { statusCode: res.status, body: res.body } };
        break;
      }
      case 'customFunction': {
        const params = {};
        for (const v of p.variableInputList.value) { params[v.name] = r.slate(v.value, true); }
        const m = { exports: null };
        // eslint-disable-next-line no-new-func
        new Function('module', 'a', '"use strict";\n' + p.code.value.replace('module.exports = a = ', 'module.exports = '))(m);
        const o = await m.exports({ params });
        const declared = Object.keys(JSON.parse(p.outputSchema.value).properties);
        for (const k of Object.keys(o)) { assert.ok(declared.indexOf(k) >= 0, title + ' returns undeclared output ' + k); }
        r.outputs[id] = o;
        break;
      }
      case 'AEqualsB':
        result = String(r.slate(p.leftValueA.value)) === String(r.slate(p.rightValueB.value));
        r.outputs[id] = { result };
        break;
      case 'saveFlowValue':
        for (const item of p.saveFlowVariables.value) {
          let v = r.slate(item.value, true);
          if (item.type === 'object' && typeof v === 'string') { v = JSON.parse(v); }
          if (item.type === 'number') { v = Number(v); }
          r.flowVars[item.name] = v;
        }
        r.outputs[id] = {};
        break;
      case 'startUiSubFlow': {
        // Flow Conductor: each input of the subflow's schema is a node property of that name.
        const sub = BY_ID[p.subFlowId.value.value];
        assert.ok(sub, title + ': subflow not in the file');
        const params = {};
        for (const k of Object.keys(p)) {
          if (['nodeTitle', 'subFlowId', 'subFlowVersionId'].indexOf(k) < 0) { params[k] = r.slate(p[k].value, true); }
        }
        for (const s of sub.inputSchema || []) { assert.ok(s.propertyName in params, title + ' does not pass ' + s.propertyName); }
        st.subflows.push(sub.name);
        const subRun = makeRun(sub, {}, world.secrets, params, !!opts.stringMode);
        const res = await runGraph(sub, subRun, world, form, opts, st);
        if (res.end !== 'success') { return res; }
        // As JSON across the response: objects come back as parsed JSON.
        r.outputs[id] = JSON.parse(JSON.stringify(res.claims));
        break;
      }
      case 'createSuccessResponse': {
        const claims = {};
        for (const c of p.claimsNameValuePairs.value) { claims[c.name] = r.slate(c.value, true); }
        const declared = Object.keys(((flow.outputSchemaCompiled || {}).output || {}).properties || {});
        for (const k of Object.keys(claims)) { assert.ok(declared.indexOf(k) >= 0, flow.name + ' returns ' + k + ' outside its output schema'); }
        return { end: 'success', claims };
      }
      case 'customHTMLTemplate': {
        const html = r.template(p.customHTML.value);
        st.calls = 0;
        st.pages.push({ title, html, flow: flow.name });
        // Final screens (styled error or notice) end the run like DaVinci's error and message nodes did.
        const kind = /data-pp-kind="(error|message)"/.exec(html);
        if (kind) {
          const text = html.replace(/<style>[\s\S]*?<\/style>/, '').replace(/<[^>]+>/g, ' ').replace(/\s+/g, ' ').trim();
          return { end: kind[1], title, text, html };
        }
        const fields = p.formFieldsList.value.map((f) => f.propertyName);
        // Self-continuing "working" pages submit themselves in the browser.
        const answer = html.indexOf('<!-- pp-auto-continue -->') >= 0 ? { buttonValue: 'CONTINUE' } : form[title];
        if (fields.length === 0 || answer === undefined) { return { end: 'page', title, html }; }
        r.outputs[id] = {};
        for (const f of fields) { r.outputs[id][f] = answer[f] === undefined ? '' : answer[f]; }
        break;
      }
      case 'customHtmlMessage':
        return { end: 'message', title, text: r.slate(p.message.value) };
      case 'customErrorMessage':
        return { end: 'error', title, text: r.slate(p.errorMessage.value) };
      default:
        throw new Error('unsupported capability ' + n.capabilityName);
    }
    const evs = out[id] || [];
    assert.ok(evs.length === 1, title + ' must lead to exactly one evaluator');
    const ev = nodes[evs[0]];
    const falseTargets = Object.keys(ev.properties || {}).filter((k) => ev.properties[k].value === 'allTriggersFalse');
    const targets = (out[ev.id] || []).filter((t) => (result ? falseTargets.indexOf(t) < 0 : falseTargets.indexOf(t) >= 0));
    assert.strictEqual(targets.length, 1, title + ': exactly one ' + (result ? 'true' : 'false') + ' branch');
    id = targets[0];
  }
  throw new Error(flow.name + ' did not finish');
}

// ---------------- fake PingOne + GitLab ----------------
function world(overrides) {
  const o = overrides || {};
  const res = (env, p, rid, conf) => ({ id: rid, url: `${O}/v1/environments/${env}/${p}`, conf });
  const st = {
    user: o.user || 'alice',
    envs: {
      [SRC]: { org: 'org', resources: [res(SRC, 'flows/f1', 'f1', { name: 'Login', v: 2, html: '<b>&</b>' }),
        res(SRC, 'applications/a1', 'a1', { name: 'App', clientSecret: 's3cr3t', settings: [{ isSensitive: true, value: 'hidden-v' }] }),
        res(SRC, 'applications/a2', 'a2', { name: 'New app', ratio: 1e-7 }), res(SRC, 'applications/a3', 'a3', { name: 'Other' })] },
      [TGT]: { org: 'org', resources: [res(TGT, 'flows/tf1', 'tf1', { name: 'Login', v: 1 }), res(TGT, 'applications/ta1', 'ta1', { name: 'App' })] }
    },
    snaps: {}, polls: {}, n: 0,
    log: [],
    gitlab: { projectId: 42, project: { id: 42, visibility: 'private', archived: false, default_branch: 'main', web_url: G + '/platform/promote' },
      branchInfo: { name: 'main', protected: true }, branches: {}, commits: [], mrs: [], fail: {} }
  };
  st.vars = { decls: {}, defs: { [SRC]: [], [TGT]: [] }, fail: 0 };
  if (o.vars) { o.vars(st.vars); }
  if (o.bigEnv) {
    for (let i = 0; i < o.bigEnv; i++) { st.envs[SRC].resources.push(res(SRC, 'applications/b' + i, 'b' + i, { name: 'Bulk ' + i })); }
  }
  if (o.gitlab) { o.gitlab(st.gitlab); }
  const json = (status, body) => ({ status, body });
  const snapshot = (env) => {
    const sid = 'snap' + (++st.n);
    st.snaps[sid] = { env, resources: JSON.parse(JSON.stringify(st.envs[env].resources)) };
    st.polls[sid] = o.snapshotPolls === undefined ? 2 : o.snapshotPolls;
    return sid;
  };
  const snapRecord = (sid) => ({ snapshotId: sid, resource: { id: st.snaps[sid].env }, status: st.polls[sid]-- > 0 ? 'PREPARING' : 'COMPLETE' });
  const pageOf = (sid, page) => {
    const items = st.snaps[sid].resources.map((x) => ({ resource: { id: x.id }, resourceUrl: x.url, configuration: x.conf, referencedResources: [] }));
    const body = { count: items.length, _embedded: { referencedResources: items.slice(page * 2, page * 2 + 2) } };
    if (page * 2 + 2 < items.length) { body._links = { next: { href: '?page=' + (page + 1) } }; }
    return body;
  };
  function pingone(method, url, headers, body) {
    assert.strictEqual(headers.Authorization, 'Bearer worker-token', 'PingOne calls use the worker token');
    const p = url.slice(O.length).split('?')[0];
    const q = url.indexOf('?') >= 0 ? url.split('?')[1] : '';
    let m;
    if ((m = /^\/v1\/environments\/([\w-]+)$/.exec(p)) && method === 'GET') { if (st.denyEnv === m[1]) { return json(403, { code: 'ACCESS_FAILED', message: 'denied' }); } return json(200, { id: m[1], organization: { id: st.envs[m[1]].org } }); }
    if ((m = /^\/v1\/environments\/([\w-]+)\/snapshots$/.exec(p)) && method === 'POST') {
      assert.strictEqual(q, 'expand=all');
      assert.strictEqual(JSON.parse(body).baseResourceURL, `${O}/v1/environments/${m[1]}`);
      return json(201, snapRecord(snapshot(m[1])));
    }
    if ((m = /^\/v1\/environments\/[\w-]+\/snapshots\/[\w-]+\/versions\/(\w+)$/.exec(p))) { return json(200, snapRecord(m[1])); }
    if ((m = /^\/v1\/environments\/[\w-]+\/snapshots\/[\w-]+\/versions\/(\w+)\/embeddedSnapshots$/.exec(p))) { return json(200, pageOf(m[1], q ? Number(q.split('=')[1]) : 0)); }
    if ((m = new RegExp('^/v1/environments/' + ADMINS + '/users/(\\w+)/roleAssignments$').exec(p))) {
      return json(200, { _embedded: { roleAssignments: m[1] === 'outsider' ? [] : [{ role: { id: ROLE }, scope: { type: 'ORGANIZATION', id: 'org' } }] } });
    }
    if (p === '/v1/roles') { return json(200, { _embedded: { roles: [{ id: ROLE, name: 'DaVinci Admin' }] } }); }
    if ((m = /^\/v1\/environments\/([\w-]+)\/promotionVariableDeclarations\/(\w+)$/.exec(p)) && method === 'GET') {
      assert.ok(st.envs[m[1]].resources.some((x) => x.id === m[2]), 'declarations are read in the environment the resource comes from (the source)');
      if (st.vars.fail) { return json(st.vars.fail, { code: 'ACCESS_FAILED' }); }
      const d = st.vars.decls[m[2]];
      return d ? json(200, { resource: { id: m[2] }, environment: { id: m[1] }, variables: d }) : json(404, { code: 'NOT_FOUND' });
    }
    if ((m = /^\/v1\/environments\/([\w-]+)\/promotionVariableDefinitions$/.exec(p)) && method === 'GET') {
      if (st.vars.fail) { return json(st.vars.fail, { code: 'ACCESS_FAILED' }); }
      const all = st.vars.defs[m[1]];
      const page = q ? Number(q.split('=')[1]) : 0;
      const body = { count: all.length, size: 1, _embedded: { variableDefinitions: all.slice(page, page + 1) } };
      if (page + 1 < all.length) { body._links = { next: { href: '?page=' + (page + 1) } }; }
      return json(200, body);
    }
    if (/\/promotions/.test(p)) { throw new Error('the Request flow must never call the promotions API'); }
    return json(404, { code: 'NOT_FOUND' });
  }
  function gitlab(method, url, headers, body) {
    assert.strictEqual(headers['PRIVATE-TOKEN'], 'glpat-test', 'GitLab calls use the GitLab token');
    assert.strictEqual(headers.Accept, 'application/json');
    const gl = st.gitlab;
    const p = url.slice((G + '/api/v4').length);
    const fail = gl.fail[method + ' ' + p.replace(/^\/projects\/\d+/, '')];
    if (fail) { return json(fail, { message: 'failed' }); }
    if (method === 'POST') { assert.strictEqual(headers['Content-Type'], 'application/json'); }
    const b = body === undefined ? undefined : JSON.parse(body);
    if (method === 'GET' && p === '/projects/' + gl.projectId) { return json(200, gl.project); }
    if (method === 'GET' && p === '/projects/' + gl.projectId + '/repository/branches/main') { return json(200, gl.branchInfo); }
    if (method === 'POST' && p === '/projects/' + gl.projectId + '/repository/branches') {
      if (gl.branches[b.branch]) { return json(400, { message: 'Branch already exists' }); }
      gl.branches[b.branch] = b.ref;
      return json(201, { name: b.branch });
    }
    if (method === 'POST' && p === '/projects/' + gl.projectId + '/repository/commits') {
      assert.ok(gl.branches[b.branch], 'commit goes to the created branch');
      gl.commits.push(b);
      return json(201, { id: 'c1' });
    }
    if (method === 'POST' && p === '/projects/' + gl.projectId + '/merge_requests') {
      gl.mrs.push(b);
      return json(201, { id: 900, iid: gl.mrs.length, web_url: gl.mrUrl || (G + '/platform/promote/-/merge_requests/' + gl.mrs.length) });
    }
    return json(404, { message: '404 Not Found' });
  }
  st.http = (method, url, headers, body) => {
    st.log.push(method + ' ' + url);
    if (url === `${A}/${ADMINS}/as/userinfo`) {
      assert.strictEqual(headers.Authorization, 'Bearer at-' + st.user);
      return json(200, { sub: st.user, preferred_username: st.user + '@example.com' });
    }
    if (url === `${A}/${HOST}/as/token`) {
      if (st.tokenStatus) { return json(st.tokenStatus, { code: 'INVALID_REQUEST' }); }
      assert.strictEqual(headers.Authorization, 'Basic d29ya2VyOnNlY3JldA==');
      assert.strictEqual(body, 'grant_type=client_credentials');
      return json(200, { access_token: 'worker-token' });
    }
    if (url.indexOf(O + '/') === 0) { return pingone(method, url, headers, body); }
    if (url.indexOf(G + '/') === 0) { return gitlab(method, url, headers, body); }
    throw new Error('unexpected host: ' + url);
  };
  st.settings = Object.assign({ apiOrigin: O, authOrigin: A, adminEnvironmentId: ADMINS, hostEnvironmentId: HOST,
    env1Name: 'Dev', env1Id: SRC, env2Name: 'Prod', env2Id: TGT, env3Name: '', env3Id: '', env4Name: '', env4Id: '',
    organizationId: 'org', davinciAdminRoleId: '', sourceRegion: 'us', targetRegion: 'us', gitlabUrl: G,
    gitlabProjectId: '42', gitlabEnvironment: 'production' }, o.settings || {});
  st.secrets = { promoteWorkerBasicAuth: 'd29ya2VyOnNlY3JldA==', promoteGitlabToken: 'glpat-test' };
  return st;
}

const form = (selectedRaw, mappingsRaw, description, button, envs) => ({
  'Choose source environment': { buttonValue: (envs || ['ENV1', 'ENV2'])[0] },
  'Choose target environment': { buttonValue: (envs || ['ENV1', 'ENV2'])[1] },
  'Choose resources': { selectedRaw, mappingsRaw: mappingsRaw || '', description: description === undefined ? 'Promote login' : description, buttonValue: 'BUILD' },
  'Review request': { buttonValue: button || 'SUBMIT' }
});
const gitlabWrites = (w) => w.log.filter((l) => l.indexOf('POST ' + G) === 0);

(async () => {
  let n = 0;
  const ok = (c, m) => { assert.ok(c, m); n++; };
  let saved = 0;

  for (const stringMode of [false, true]) {
    const mode = stringMode ? ' (JSON-string inputs)' : '';
    // Happy path.
    const w = world();
    const r = await runFlow(w, form('f1 a1 a2', 'f1=tf1\na1=ta1', 'Fix <login> & "MFA"'), { stringMode });
    ok(r.end === 'page' && r.title === 'Merge request opened', 'happy path ends on the MR page' + mode + ': ' + (r.text || ''));
    ok(r.html.indexOf(G + '/platform/promote/-/merge_requests/1') >= 0, 'MR link shown' + mode);
    const gl = w.gitlab;
    const id = Object.keys(gl.branches)[0].slice('promotion/'.length);
    ok(/^[a-f0-9]{32}$/.test(id) && gl.branches['promotion/' + id] === 'main', 'branch promotion/<id> from the default branch' + mode);
    const c = gl.commits[0];
    ok(c.branch === 'promotion/' + id && c.start_branch === 'main' && c.commit_message === 'Add promotion request ' + id, 'commit metadata as Go' + mode);
    ok(c.actions.length === 1 && c.actions[0].action === 'create' && c.actions[0].file_path === 'requests/' + id + '/request.json', 'one create action at requests/<id>/request.json' + mode);
    const content = c.actions[0].content;
    ok(content.slice(-2) === '}\n' && content.indexOf('\n  "version": 1,') > 0, 'content is indented JSON plus a trailing newline' + mode);
    ok(content.indexOf('s3cr3t') < 0 && content.indexOf('hidden-v') < 0 && content.indexOf('[REDACTED]') > 0, 'secrets redacted in the commit' + mode);
    ok(content.indexOf('\\u003cb\\u003e\\u0026\\u003c/b\\u003e') > 0 && content.indexOf('1e-7') > 0, 'Go escaping and number format in the commit' + mode);
    const req = JSON.parse(content);
    ok(req.selected.join() === 'a1,a2,f1' && req.mappings.f1 === 'tf1' && req.source.region === 'us' && req.target.snapshotId, 'request content' + mode);
    const mr = gl.mrs[0];
    ok(mr.source_branch === 'promotion/' + id && mr.target_branch === 'main' && mr.remove_source_branch === true && mr.squash === false, 'MR branches and flags as Go' + mode);
    ok(mr.title === 'Fix <login> & "MFA"', 'MR title is the description' + mode);
    ok(mr.description.indexOf('Promotion request `' + id + '`\n\nSource environment: `' + SRC + '` (snapshot `' + req.source.snapshotId + '`)\nTarget environment: `' + TGT + '`\n' +
      'Selected resources: 3\nGitLab environment: `production`\n\nThis merge request submits configuration for review.') === 0, 'MR description as Go' + mode);
    if (SIGNIN) {
      ok(/Requested in DaVinci by: `alice@example\.com \(PingOne user alice\)`\n\n### Promotion variables/.test(mr.description), 'MR names the signed-in admin' + mode);
    } else {
      ok(mr.description.indexOf('Requested in DaVinci by') < 0 && /approving deployment\.\n\n### Promotion variables/.test(mr.description), 'sign-in off: MR names no requester' + mode);
      ok(w.log.every((l) => l.indexOf('userinfo') < 0 && l.indexOf('roleAssignments') < 0), 'sign-in off: no userinfo or role calls' + mode);
    }
    ok(mr.description.indexOf('2 of 2 sensitive value(s) need attention') > 0 && mr.description.indexOf('- `a1` `clientSecret`: needs a sensitive promotion variable') > 0 &&
      mr.description.indexOf('- `a1` `settings[0].value`: needs') > 0, 'MR lists the missing promotion variables' + mode);
    const preview = r.pages.find((pg) => pg.title === 'Review request').html;
    ok(preview.indexOf('requests/' + id + '/request.json') >= 0 && preview.indexOf('&quot;version&quot;: 1') >= 0 && preview.indexOf('"version"') < 0 && preview.indexOf('s3cr3t') < 0, 'preview shows the escaped, redacted file' + mode);
    const sel = r.pages.find((pg) => pg.title === 'Choose resources').html;
    ok(sel.indexOf('a1') >= 0 && sel.indexOf('s3cr3t') < 0 && (SIGNIN ? sel.indexOf('alice@example.com') >= 0 : sel.indexOf('Sign-in is off') >= 0), 'selection page lists IDs and who (or that sign-in is off), never configuration' + mode);
    ok(w.log.indexOf('GET ' + G + '/api/v4/projects/42') < w.log.indexOf('POST ' + G + '/api/v4/projects/42/repository/branches'), 'project verified before any write' + mode);
    if (SAVE_DIR) { fs.writeFileSync(path.join(SAVE_DIR, 'flow-' + (saved++) + '.json'), content); }
  }

  // Long description: MR title cut at 180 bytes like Go.
  {
    const w = world();
    const d = 'é'.repeat(95);
    await runFlow(w, form('a2', '', d));
    ok(w.gitlab.mrs[0].title === 'é'.repeat(90), 'title truncated to 180 bytes');
    ok(JSON.parse(w.gitlab.commits[0].actions[0].content).description === d, 'request keeps the full description');
    if (SAVE_DIR) { fs.writeFileSync(path.join(SAVE_DIR, 'flow-' + (saved++) + '.json'), w.gitlab.commits[0].actions[0].content); }
    const w2 = world();
    await runFlow(w2, form('a2', '', 'x'.repeat(179) + 'é'));
    ok(w2.gitlab.mrs[0].title === 'x'.repeat(179) + '�', 'a cut character becomes U+FFFD');
    const w3 = world();
    await runFlow(w3, form('a2', '', ''));
    ok(w3.gitlab.mrs[0].title === 'Promote selected PingOne resources', 'empty description uses the default title');
  }

  // Promotion-variable pre-check (advisory: it never blocks the submission).
  const varsRun = async (selected, tweak) => {
    const w = world({ vars: tweak });
    const r = await runFlow(w, form(selected));
    ok(r.title === 'Merge request opened', 'variables check never blocks: ' + (r.text || ''));
    const review = r.pages.find((pg) => pg.title === 'Review request').html;
    return { w, review, mr: w.gitlab.mrs[0].description };
  };
  {
    const { review, mr } = await varsRun('a1', (v) => {
      v.decls.a1 = { clientSecret: '${promotionVariable: appSecret}', settings: '${promotionVariable:settingsVal}' };
      v.defs[TGT] = [{ id: 'd1', name: 'appSecret', value: '******', isSensitive: true }, { id: 'd2', name: 'settingsVal', value: '******', variableType: 'ENCRYPTED_STRING' }];
    });
    ok(mr.indexOf('All 2 sensitive value(s) have a sensitive promotion variable.') > 0 && mr.indexOf('ready: sensitive promotion variable `appSecret` is defined') > 0, 'declared and defined: ready');
    ok(review.indexOf('All 2 sensitive value(s)') >= 0 && review.indexOf('settings[0].value: ready') >= 0, 'review page shows the check');
    ok(review.indexOf('s3cr3t') < 0 && mr.indexOf('s3cr3t') < 0 && review.indexOf('hidden-v') < 0, 'no secret value anywhere in the check');
  }
  {
    const { mr } = await varsRun('a1', (v) => {
      v.decls.a1 = { clientSecret: 'appSecret', 'settings[0]': '${promotionVariable: plainVar}' };
      v.defs[SRC] = [{ id: 'd3', name: 'plainVar', value: 'x' }];
    });
    ok(mr.indexOf('`clientSecret`: declared as `appSecret`, but no definition was found') > 0, 'declared without a definition');
    ok(mr.indexOf('`settings[0].value`: declared as `plainVar`, but its definition is not marked sensitive') > 0, 'definition not sensitive; ancestor property covers the path');
    ok(mr.indexOf('2 of 2 sensitive value(s) need attention') > 0, 'both counted as open');
  }
  {
    const { review, mr } = await varsRun('a1', (v) => { v.fail = 403; });
    ok(mr.indexOf('could not be checked') > 0 && mr.indexOf('Source variable definitions could not be read') > 0 && mr.indexOf('1 resource declaration(s) could not be read') > 0, 'unreadable API reported, not fatal');
    ok(review.indexOf('could not be checked') >= 0, 'review page says it could not check');
  }
  {
    const { w, mr } = await varsRun('f1 a2', () => {});
    ok(mr.indexOf('No sensitive values in the selected resources') > 0, 'no sensitive values: nothing needed');
    const read = w.log.filter((l) => /promotionVariableDeclarations/.test(l)).map((l) => l.split('/').pop());
    ok(read.join() === 'a2,f1', 'each selected resource\'s declaration read once, by ID: ' + read.join());
  }
  {
    const { w } = await varsRun('a1', (v) => { v.defs[TGT] = [{ name: 'a', value: '******', isSensitive: true }, { name: 'b', value: 1 }, { name: 'c', value: 2 }]; });
    ok(w.log.filter((l) => l.indexOf(TGT + '/promotionVariableDefinitions') > 0).length === 3, 'every definitions page is read');
  }

  // Long work is split across browser requests: big environment, slow snapshot.
  {
    const w = world({ bigEnv: 60, snapshotPolls: 40 });
    const r = await runFlow(w, form('b7'));
    ok(r.title === 'Merge request opened', 'big environment and slow snapshot still complete: ' + (r.text || ''));
    ok(r.run.maxCalls <= 8, 'no browser request makes more than 8 REST calls (max ' + r.run.maxCalls + ')');
    ok(r.pages.filter((pg) => pg.html.indexOf('pp-auto-continue') >= 0).length >= 40, 'snapshot polls and page reads hand back to the browser');
    const many = Array.from({ length: 30 }, (_, i) => 'b' + i).join(' ');
    const w2 = world({ bigEnv: 60 });
    const r2 = await runFlow(w2, form(many));
    ok(r2.title === 'Merge request opened' && r2.run.maxCalls <= 8, '30 selected resources: declarations read in short requests too (max ' + r2.run.maxCalls + ')');
  }

  // Refusals that must never reach GitLab.
  const noGitLab = async (name, w, f, expect) => {
    const r = await runFlow(w, f);
    ok(r.end !== 'page' || r.title !== 'Merge request opened', name + ': not submitted');
    ok(w.log.every((l) => l.indexOf(G) < 0), name + ': no GitLab call');
    if (expect) { ok((r.text || '').indexOf(expect) >= 0, name + ': says "' + expect + '" (got: ' + r.text + ')'); }
    return r;
  };
  if (SIGNIN) {
    await noGitLab('non-admin', world({ user: 'outsider' }), form('a1'), 'Access denied');
    ok(world({ user: 'outsider' }).log.length === 0, 'fresh world has no calls');
    const w = world({ user: 'outsider' });
    await runFlow(w, form('a1'));
    ok(w.log.every((l) => l.indexOf('/snapshots') < 0), 'non-admin: no snapshots taken');
  }
  // Choosing environments.
  {
    const w = world();
    const r = await runFlow(w, form('a2'));
    const pick = r.pages.find((pg) => pg.title === 'Choose source environment').html;
    ok(pick.indexOf('<b>Dev</b>') > 0 && pick.indexOf('<b>Prod</b>') > 0 && (pick.match(/data-pp-env="1"/g) || []).length === 4, 'source page offers one button per slot, labelled by name');
    const tpage = r.pages.find((pg) => pg.title === 'Choose target environment').html;
    ok(tpage.indexOf('Promote from <strong>Dev</strong>') > 0 && tpage.indexOf('var hide = "Dev"') > 0, 'target page names the source and hides it');
    ok(r.pages.find((pg) => pg.title === 'Choose resources').html.indexOf('<span class="pp-chip">Dev</span>&rarr;<span class="pp-chip">Prod</span>') > 0, 'resource page shows the chosen pair');
    ok(JSON.parse(w.gitlab.commits[0].actions[0].content).source.id === SRC, 'request uses the chosen source');
  }
  {
    const w = world();
    const r = await runFlow(w, form('tf1', '', 'reverse', 'SUBMIT', ['ENV2', 'ENV1']));
    const req = JSON.parse(w.gitlab.commits[0].actions[0].content);
    ok(r.title === 'Merge request opened' && req.source.id === TGT && req.target.id === SRC, 'either direction: Prod to Dev');
  }
  // One promotion project; each target can name its GitLab environment.
  {
    const w = world({ settings: { env2GitlabEnvironment: 'prod' } });
    const r = await runFlow(w, form('a2'));
    const mr = w.gitlab.mrs[0];
    ok(r.title === 'Merge request opened' && w.log.some((l) => l.indexOf('/api/v4/projects/42/merge_requests') > 0) &&
      mr.description.indexOf('GitLab environment: `prod`') > 0, 'target Prod names its GitLab environment, same project: ' + (r.text || ''));
  }
  {
    const w = world({ settings: { env2GitlabEnvironment: 'prod' } });
    const r = await runFlow(w, form('tf1', '', 'reverse', 'SUBMIT', ['ENV2', 'ENV1']));
    ok(r.title === 'Merge request opened' && w.gitlab.mrs[0].description.indexOf('GitLab environment: `production`') > 0,
      'a target without its own GitLab environment uses gitlabEnvironment');
  }
  await noGitLab('bad GitLab environment name', world({ settings: { env2GitlabEnvironment: 'prod env' } }), form('a2'), 'env2GitlabEnvironment must be a GitLab environment name');
  await noGitLab('promotion project missing', world({ settings: { gitlabProjectId: '' } }), form('a2'), 'gitlabProjectId must be the numeric ID');
  await noGitLab('same environment twice', world(), form('a2', '', 'x', 'SUBMIT', ['ENV1', 'ENV1']), 'must be different');
  await noGitLab('empty slot chosen', world(), form('a2', '', 'x', 'SUBMIT', ['ENV3', 'ENV1']), 'not configured');
  await noGitLab('unsafe ID in a slot', world({ settings: { env3Name: 'Bad', env3Id: '../x' } }), form('a2', '', 'x', 'SUBMIT', ['ENV3', 'ENV1']), 'env3Id must be an environment ID');
  // The settings check runs first and names placeholder or malformed variables.
  for (const [what, settings, msg] of [
    ['placeholder host environment', { hostEnvironmentId: 'REPLACE_WITH_HOST_ENVIRONMENT_ID' }, 'hostEnvironmentId must be the ID'],
    ['auth origin with a path', { authOrigin: A + '/' }, 'authOrigin must be like https://auth.pingone.com'],
    ['custom domain as api origin', { apiOrigin: 'https://login.example.com' }, 'apiOrigin must be like'],
    ['only one environment', { env2Name: '', env2Id: '' }, 'Set at least two environments'],
    ['name missing for a slot', { env3Id: '55555555-5555-4555-8555-555555555555' }, 'env3Name is empty'],
    ['plain-http GitLab', { gitlabUrl: 'http://gitlab.example.com' }, 'gitlabUrl must be an HTTPS URL']
  ]) {
    const w = world({ settings });
    const r = await runFlow(w, form('a2'));
    ok(r.title === 'Settings need attention' && (r.text || '').indexOf(msg) >= 0 && w.log.length === 0,
      'settings check: ' + what + ' is named before any API call: ' + (r.text || r.title));
  }
  await noGitLab('forged button value', world(), form('a2', '', 'x', 'SUBMIT', ['ENV9', 'ENV1']), 'Choose one of the listed');
  {
    const w = world();
    await runFlow(w, form('a2', '', 'x', 'SUBMIT', ['ENV1', 'ENV1']));
    ok(w.log.every((l) => l.indexOf('/snapshots') < 0), 'no snapshot before both environments are valid');
  }

  await noGitLab('cancel at review', world(), form('a1', '', 'x', 'CANCEL'), 'Nothing was sent to GitLab');
  await noGitLab('unknown resource', world(), form('nope'), 'unknown source resource');
  await noGitLab('duplicate resource', world(), form('a1 a1'), 'duplicate');
  await noGitLab('type-mismatched mapping', world(), form('a1', 'a1=tf1'), 'incompatible');
  await noGitLab('unsafe ID', world(), form('../x'), 'not a valid ID');
  await noGitLab('escaped echo', world(), form('<img>'), '&lt;img&gt;');
  await noGitLab('region unset', world({ settings: { sourceRegion: '' } }), form('a1'), 'region');
  await noGitLab('http GitLab URL', world({ settings: { gitlabUrl: 'http://gitlab.example.com' } }), form('a1'), 'HTTPS');
  await noGitLab('GitLab URL with credentials', world({ settings: { gitlabUrl: 'https://u:p@gitlab.example.com' } }), form('a1'), 'HTTPS');
  await noGitLab('GitLab URL traversal', world({ settings: { gitlabUrl: G + '/../x' } }), form('a1'), 'traversal');
  await noGitLab('non-numeric project', world({ settings: { gitlabProjectId: '42/../7' } }), form('a1'), 'numeric');
  await noGitLab('bad environment name', world({ settings: { gitlabEnvironment: 'prod env' } }), form('a1'), 'environment name');
  await noGitLab('long description', world(), form('a1', '', 'x'.repeat(4001)), 'too long');

  // GitLab checks: reads may happen, writes must not.
  const noWrite = async (name, tweak, expect) => {
    const w = world({ gitlab: tweak });
    const r = await runFlow(w, form('a1'));
    ok(r.end === 'error' && r.text.indexOf(expect) >= 0, name + ': "' + expect + '" (got: ' + r.text + ')');
    ok(gitlabWrites(w).length === 0, name + ': no GitLab write');
  };
  await noWrite('public project', (g) => { g.project.visibility = 'internal'; }, 'must be private');
  await noWrite('archived project', (g) => { g.project.archived = true; }, 'must be private');
  await noWrite('no default branch', (g) => { g.project.default_branch = ''; }, 'default branch');
  await noWrite('different project', (g) => { g.project.id = 43; }, 'different project');
  await noWrite('project on another host', (g) => { g.project.web_url = 'https://evil.example/platform/promote'; }, 'outside the configured server');
  await noWrite('project outside base path', (g) => { g.project.web_url = 'https://gitlab.example.com/other/promote'; }, 'outside the configured server');
  await noWrite('project URL traversal', (g) => { g.project.web_url = G + '/%2e%2e/x'; }, 'outside the configured server');
  await noWrite('unprotected default branch', (g) => { g.branchInfo.protected = false; }, 'must be protected');
  await noWrite('branch name mismatch', (g) => { g.branchInfo.name = 'other'; }, 'must be protected');
  await noWrite('project unreadable', (g) => { g.fail['GET '] = 401; }, 'HTTP 401');

  // A failed API call ends on an error naming the step, never DaVinci's generic error page.
  for (const drop of [false, true]) {
    const sfx = drop ? ' (failed step keeps no output)' : '';
    {
      const w = world(); w.denyEnv = TGT; w.dropFailedOutput = drop;
      const r = await runFlow(w, form('a1'));
      ok(r.end === 'error' && r.text.indexOf('Snapshot: read environment" (Prod environment)') >= 0 && r.text.indexOf('no role on that environment') >= 0 &&
        w.log.every((l) => l.indexOf(TGT + '/snapshots') < 0), 'target environment 403 is named' + sfx + ': ' + (r.text || r.title));
    }
    {
      const w = world(); w.tokenStatus = 400; w.dropFailedOutput = drop;
      const r = await runFlow(w, form('a1'));
      ok(r.end === 'error' && r.text.indexOf('Promotion worker token') >= 0 && r.text.indexOf('hostEnvironmentId') >= 0 && w.log.every((l) => l.indexOf('/v1/') < 0),
        'token 400 is named' + sfx + ': ' + (r.text || r.title));
    }
    {
      const w = world({ gitlab: (g) => { g.fail['POST /repository/branches'] = 400; } }); w.dropFailedOutput = drop;
      const r = await runFlow(w, form('a1'));
      ok(r.end === 'error' && r.text.indexOf('create promotion branch') >= 0 && w.gitlab.commits.length === 0, 'GitLab failure still stops at its step' + sfx);
    }
  }
  // Main flow + subflows: each step runs in its own subflow, in order; admin settings live only in the main flow.
  {
    const w = world();
    const r = await runFlow(w, form('a1'));
    ok(r.title === 'Merge request opened' && JSON.stringify(r.subflows) === JSON.stringify([
      'PingOne Promote - 1 Choose environments', 'PingOne Promote - 2 Snapshot environment', 'PingOne Promote - 2 Snapshot environment',
      'PingOne Promote - 3 Choose resources and review', 'PingOne Promote - 4 Submit to GitLab']), 'subflows run in order: ' + JSON.stringify(r.subflows));
    ok(FLOWS.length === 5 && FLOWS.slice(1).every((fl) => fl.variables.every((v) => v.context !== 'flow' || ['apiOrigin', 'gitlabProjectId', 'env1Id', 'hostEnvironmentId'].indexOf(v.fields.displayName) < 0)),
      'subflows carry no admin settings: those are set once, in the main flow');
    ok(r.pages.some((pg) => pg.flow === 'PingOne Promote - 2 Snapshot environment' && pg.html.indexOf('Taking a snapshot of the <strong>Prod</strong> environment') >= 0),
      'the snapshot subflow names the environment it is working on');
    ok(FLOWS.every((fl) => (fl.settings || {}).logLevel === 3), 'every flow logs at Debug');
  }
  {
    const w = world();
    const r = await runFlow(w, form('a1', '', 'x', 'CANCEL'));
    ok(r.end === 'message' && r.subflows.indexOf('PingOne Promote - 4 Submit to GitLab') < 0, 'cancel ends in the review subflow; submit never runs');
  }
  // Partial failures stop at the failed step.
  {
    const w = world({ gitlab: (g) => { g.fail['POST /repository/branches'] = 400; } });
    const r = await runFlow(w, form('a1'));
    ok(r.end === 'error' && r.text.indexOf('create promotion branch: GitLab returned HTTP 400') >= 0 && w.gitlab.commits.length === 0, 'branch failure stops before commit');
  }
  {
    const w = world({ gitlab: (g) => { g.fail['POST /repository/commits'] = 500; } });
    const r = await runFlow(w, form('a1'));
    ok(r.end === 'error' && r.text.indexOf('HTTP 500') >= 0 && w.gitlab.mrs.length === 0, 'commit failure stops before the MR');
  }
  {
    const w = world({ gitlab: (g) => { g.mrUrl = 'https://evil.example/mr/1'; } });
    const r = await runFlow(w, form('a1'));
    ok(r.end === 'error' && r.text.indexOf('merge request URL outside') >= 0, 'MR URL on another host is reported, not linked');
  }
  {
    const w = world({ settings: { gitlabEnvironment: 'staging' } });
    await runFlow(w, form('a1'));
    ok(w.gitlab.mrs[0].description.indexOf('GitLab environment: `staging`') > 0, 'configured GitLab environment named in the MR');
  }
  {
    const w = world({ settings: { gitlabUrl: G + '/' } });
    const r = await runFlow(w, form('a1'));
    ok(r.title === 'Merge request opened' && w.log.indexOf('GET ' + G + '/api/v4/projects/42') >= 0, 'trailing slash in the GitLab URL is normalised');
  }
  console.log('flow run (sign-in ' + (SIGNIN ? 'on' : 'off') + '): ' + n + ' checks passed' + (SAVE_DIR ? ', ' + saved + ' committed requests saved' : ''));
})().catch((e) => { console.error('FAILED:', e.stack || e.message); process.exit(1); });
