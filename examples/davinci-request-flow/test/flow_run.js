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

// ---------------- the interpreter ----------------
const nodes = {};
for (const n of FLOW.graphData.elements.nodes) { nodes[n.data.id] = n.data; }
const out = {};
for (const e of FLOW.graphData.elements.edges) { (out[e.data.source] = out[e.data.source] || []).push(e.data.target); }
const incoming = new Set(FLOW.graphData.elements.edges.map((e) => e.data.target));
const START = Object.keys(nodes).filter((id) => !incoming.has(id));
// Sign-in is optional (off for the proof of concept); the checks follow the export.
const SIGNIN = Object.values(nodes).some((n) => n.capabilityName === 'loginFirstFactor');
assert.strictEqual(START.length, 1, 'one start node');

function get(obj, dotted) {
  return dotted.split('.').reduce((v, k) => (v === undefined || v === null ? undefined : v[k]), obj);
}

function makeRun(settings, secrets, form, stringMode) {
  // Flow variables come from the import; the settings stand in for the values the admin sets in DaVinci.
  const flowVars = {};
  for (const v of FLOW.variables) {
    if (v.context === 'flow') { flowVars[v.fields.displayName] = v.fields.value; }
  }
  Object.assign(flowVars, settings);
  const outputs = {};
  const lookup = (ref) => {
    let m;
    if ((m = /^local\.([a-z0-9]+)\.payload\.output\.(.+)$/.exec(ref))) {
      assert.ok(m[1] in outputs, 'reference to a node that has not run: ' + ref);
      return get(outputs[m[1]], m[2]);
    }
    if ((m = /^global\.flow\.variables\.(.+)$/.exec(ref))) { assert.ok(m[1] in flowVars, 'flow variable read before any node set it: ' + m[1]); return flowVars[m[1]]; }
    if ((m = /^global\.company\.variables\.(.+)$/.exec(ref))) { return secrets[m[1]]; }
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
  return { flowVars, outputs, slate, template };
}

async function runFlow(world, form, opts) {
  opts = opts || {};
  const r = makeRun(world.settings, world.secrets, form, !!opts.stringMode);
  const pages = [];
  let id = START[0];
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
        const headers = {};
        for (const h of p.headers.value) { headers[h.key] = r.slate(h.value); }
        const body = p.httpBody && p.httpBody.value === 'raw' ? r.template(p.raw.value) : undefined;
        const res = world.http(p.httpMethod.value, r.slate(p.url.value), headers, body);
        r.outputs[id] = { rawResponse: { statusCode: res.status, body: res.body } };
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
      case 'customHTMLTemplate': {
        const html = r.template(p.customHTML.value);
        pages.push({ title, html });
        const fields = p.formFieldsList.value.map((f) => f.propertyName);
        const answer = form[title];
        if (fields.length === 0 || answer === undefined) { return { end: 'page', title, html, pages, run: r }; }
        r.outputs[id] = {};
        for (const f of fields) { r.outputs[id][f] = answer[f] === undefined ? '' : answer[f]; }
        break;
      }
      case 'customHtmlMessage':
        return { end: 'message', title, text: r.slate(p.message.value), pages, run: r };
      case 'customErrorMessage':
        return { end: 'error', title, text: r.slate(p.errorMessage.value), pages, run: r };
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
  throw new Error('flow did not finish');
}

// ---------------- fake PingOne + GitLab ----------------
function world(overrides) {
  const o = overrides || {};
  const res = (env, p, rid, conf) => ({ id: rid, url: `${O}/v1/environments/${env}/${p}`, conf });
  const st = {
    user: o.user || 'alice',
    envs: {
      src: { org: 'org', resources: [res('src', 'flows/f1', 'f1', { name: 'Login', v: 2, html: '<b>&</b>' }),
        res('src', 'applications/a1', 'a1', { name: 'App', clientSecret: 's3cr3t', settings: [{ isSensitive: true, value: 'hidden-v' }] }),
        res('src', 'applications/a2', 'a2', { name: 'New app', ratio: 1e-7 }), res('src', 'applications/a3', 'a3', { name: 'Other' })] },
      tgt: { org: 'org', resources: [res('tgt', 'flows/tf1', 'tf1', { name: 'Login', v: 1 }), res('tgt', 'applications/ta1', 'ta1', { name: 'App' })] }
    },
    snaps: {}, polls: {}, n: 0,
    log: [],
    gitlab: { project: { id: 42, visibility: 'private', archived: false, default_branch: 'main', web_url: G + '/platform/promote' },
      branchInfo: { name: 'main', protected: true }, branches: {}, commits: [], mrs: [], fail: {} }
  };
  st.vars = { decls: {}, defs: { src: [], tgt: [] }, fail: 0 };
  if (o.vars) { o.vars(st.vars); }
  if (o.gitlab) { o.gitlab(st.gitlab); }
  const json = (status, body) => ({ status, body });
  const snapshot = (env) => {
    const sid = 'snap' + (++st.n);
    st.snaps[sid] = { env, resources: JSON.parse(JSON.stringify(st.envs[env].resources)) };
    st.polls[sid] = 2;
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
    if ((m = /^\/v1\/environments\/(\w+)$/.exec(p)) && method === 'GET') { return json(200, { id: m[1], organization: { id: st.envs[m[1]].org } }); }
    if ((m = /^\/v1\/environments\/(\w+)\/snapshots$/.exec(p)) && method === 'POST') {
      assert.strictEqual(q, 'expand=all');
      assert.strictEqual(JSON.parse(body).baseResourceURL, `${O}/v1/environments/${m[1]}`);
      return json(201, snapRecord(snapshot(m[1])));
    }
    if ((m = /^\/v1\/environments\/\w+\/snapshots\/\w+\/versions\/(\w+)$/.exec(p))) { return json(200, snapRecord(m[1])); }
    if ((m = /^\/v1\/environments\/\w+\/snapshots\/\w+\/versions\/(\w+)\/embeddedSnapshots$/.exec(p))) { return json(200, pageOf(m[1], q ? Number(q.split('=')[1]) : 0)); }
    if ((m = /^\/v1\/environments\/admins\/users\/(\w+)\/roleAssignments$/.exec(p))) {
      return json(200, { _embedded: { roleAssignments: m[1] === 'outsider' ? [] : [{ role: { id: ROLE }, scope: { type: 'ORGANIZATION', id: 'org' } }] } });
    }
    if (p === '/v1/roles') { return json(200, { _embedded: { roles: [{ id: ROLE, name: 'DaVinci Admin' }] } }); }
    if ((m = /^\/v1\/environments\/(\w+)\/promotionVariableDeclarations\/(\w+)$/.exec(p)) && method === 'GET') {
      assert.strictEqual(m[1], 'src', 'declarations are read in the source environment');
      if (st.vars.fail) { return json(st.vars.fail, { code: 'ACCESS_FAILED' }); }
      const d = st.vars.decls[m[2]];
      return d ? json(200, { resource: { id: m[2] }, environment: { id: 'src' }, variables: d }) : json(404, { code: 'NOT_FOUND' });
    }
    if ((m = /^\/v1\/environments\/(\w+)\/promotionVariableDefinitions$/.exec(p)) && method === 'GET') {
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
    const fail = gl.fail[method + ' ' + p.replace(/^\/projects\/42/, '')];
    if (fail) { return json(fail, { message: 'failed' }); }
    if (method === 'POST') { assert.strictEqual(headers['Content-Type'], 'application/json'); }
    const b = body === undefined ? undefined : JSON.parse(body);
    if (method === 'GET' && p === '/projects/42') { return json(200, gl.project); }
    if (method === 'GET' && p === '/projects/42/repository/branches/main') { return json(200, gl.branchInfo); }
    if (method === 'POST' && p === '/projects/42/repository/branches') {
      if (gl.branches[b.branch]) { return json(400, { message: 'Branch already exists' }); }
      gl.branches[b.branch] = b.ref;
      return json(201, { name: b.branch });
    }
    if (method === 'POST' && p === '/projects/42/repository/commits') {
      assert.ok(gl.branches[b.branch], 'commit goes to the created branch');
      gl.commits.push(b);
      return json(201, { id: 'c1' });
    }
    if (method === 'POST' && p === '/projects/42/merge_requests') {
      gl.mrs.push(b);
      return json(201, { id: 900, iid: gl.mrs.length, web_url: gl.mrUrl || (G + '/platform/promote/-/merge_requests/' + gl.mrs.length) });
    }
    return json(404, { message: '404 Not Found' });
  }
  st.http = (method, url, headers, body) => {
    st.log.push(method + ' ' + url);
    if (url === `${A}/admins/as/userinfo`) {
      assert.strictEqual(headers.Authorization, 'Bearer at-' + st.user);
      return json(200, { sub: st.user, preferred_username: st.user + '@example.com' });
    }
    if (url === `${A}/host/as/token`) {
      assert.strictEqual(headers.Authorization, 'Basic d29ya2VyOnNlY3JldA==');
      assert.strictEqual(body, 'grant_type=client_credentials');
      return json(200, { access_token: 'worker-token' });
    }
    if (url.indexOf(O + '/') === 0) { return pingone(method, url, headers, body); }
    if (url.indexOf(G + '/') === 0) { return gitlab(method, url, headers, body); }
    throw new Error('unexpected host: ' + url);
  };
  st.settings = Object.assign({ apiOrigin: O, authOrigin: A, adminEnvironmentId: 'admins', hostEnvironmentId: 'host', sourceEnvironmentId: 'src',
    targetEnvironmentId: 'tgt', organizationId: 'org', davinciAdminRoleId: '', sourceRegion: 'us', targetRegion: 'us', gitlabUrl: G,
    gitlabProjectId: '42', gitlabEnvironment: 'production' }, o.settings || {});
  st.secrets = { promoteWorkerBasicAuth: 'd29ya2VyOnNlY3JldA==', promoteGitlabToken: 'glpat-test' };
  return st;
}

const form = (selectedRaw, mappingsRaw, description, button) => ({
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
    ok(mr.description.indexOf('Promotion request `' + id + '`\n\nSource environment: `src` (snapshot `' + req.source.snapshotId + '`)\nTarget environment: `tgt`\n' +
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
    ok(sel.indexOf('a1') >= 0 && sel.indexOf('s3cr3t') < 0 && (SIGNIN ? sel.indexOf('alice@example.com') >= 0 : sel.indexOf('sign-in is off') >= 0), 'selection page lists IDs and who (or that sign-in is off), never configuration' + mode);
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
      v.defs.tgt = [{ id: 'd1', name: 'appSecret', value: '******', isSensitive: true }, { id: 'd2', name: 'settingsVal', value: '******', variableType: 'ENCRYPTED_STRING' }];
    });
    ok(mr.indexOf('All 2 sensitive value(s) have a sensitive promotion variable.') > 0 && mr.indexOf('ready: sensitive promotion variable `appSecret` is defined') > 0, 'declared and defined: ready');
    ok(review.indexOf('All 2 sensitive value(s)') >= 0 && review.indexOf('settings[0].value: ready') >= 0, 'review page shows the check');
    ok(review.indexOf('s3cr3t') < 0 && mr.indexOf('s3cr3t') < 0 && review.indexOf('hidden-v') < 0, 'no secret value anywhere in the check');
  }
  {
    const { mr } = await varsRun('a1', (v) => {
      v.decls.a1 = { clientSecret: 'appSecret', 'settings[0]': '${promotionVariable: plainVar}' };
      v.defs.src = [{ id: 'd3', name: 'plainVar', value: 'x' }];
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
    const { w } = await varsRun('a1', (v) => { v.defs.tgt = [{ name: 'a', value: '******', isSensitive: true }, { name: 'b', value: 1 }, { name: 'c', value: 2 }]; });
    ok(w.log.filter((l) => /tgt\/promotionVariableDefinitions/.test(l)).length === 3, 'every definitions page is read');
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
