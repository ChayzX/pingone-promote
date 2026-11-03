// Shared helpers embedded in every feasibility custom function (as `lib`).
// gen_flow.py removes full-line comments and indentation when embedding, so
// never start a line inside a string with //.
const lib = {};

lib.VERSION = 1;
lib.now = () => Date.now();

// DaVinci hands function inputs over as objects or as JSON text; accept both.
lib.parse = (v) => {
  if (v === undefined || v === null || v === '') { return null; }
  if (typeof v === 'string') {
    const s = v.trim();
    if (s === '' || s === 'null' || s === 'undefined') { return null; }
    if (s.charAt(0) === '{' || s.charAt(0) === '[') {
      try { return JSON.parse(s); } catch (e) { return v; }
    }
    return v;
  }
  return v;
};
lib.obj = (v) => {
  const p = lib.parse(v);
  return p && typeof p === 'object' && !Array.isArray(p) ? p : {};
};
lib.str = (v) => {
  if (v === undefined || v === null) { return ''; }
  const s = String(v).trim();
  // An unresolved {{reference}} means the value was never set.
  return /^\{\{[^}]*\}\}$/.test(s) ? '' : s;
};
lib.int = (v, dflt, min, max) => {
  const s = lib.str(v);
  if (!/^[0-9]{1,9}$/.test(s)) { return dflt; }
  const n = Number(s);
  return Math.min(max, Math.max(min, n));
};

// UTF-8 byte length without Buffer or TextEncoder (neither is guaranteed in DaVinci).
lib.utf8Len = (s) => {
  let n = 0;
  for (let i = 0; i < s.length; i++) {
    const c = s.charCodeAt(i);
    if (c < 0x80) { n += 1; } else if (c < 0x800) { n += 2; } else if (c >= 0xd800 && c <= 0xdbff) { n += 4; i++; } else { n += 3; }
  }
  return n;
};
lib.size = (v) => {
  if (v === undefined || v === null) { return 0; }
  return lib.utf8Len(typeof v === 'string' ? v : JSON.stringify(v));
};

lib.escapeHtml = (s) => String(s === undefined || s === null ? '' : s)
  .replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;').replace(/'/g, '&#39;');

// Upstream error text is shown and pasted back to us, so remove anything that
// could identify the tenant or carry a credential, then cut it short.
lib.redact = (s) => String(s === undefined || s === null ? '' : s)
  .replace(/(bearer|basic)\s+[A-Za-z0-9._~+\/=-]+/gi, '$1 <redacted>')
  .replace(/glpat-[A-Za-z0-9_\-]+/g, '<redacted>')
  .replace(/eyJ[A-Za-z0-9_\-]+\.[A-Za-z0-9_\-]+\.[A-Za-z0-9_\-]*/g, '<redacted>')
  .replace(/[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}/g, '<id>')
  .replace(/\b[0-9a-fA-F]{24,64}\b/g, '<id>')
  .replace(/[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}/g, '<email>')
  .replace(/https?:\/\/[^\s"'<>]+/g, '<url>')
  .replace(/[\u0000-\u001f\u007f]+/g, ' ')
  .slice(0, 160);

// A short, safe description of a failed response: status, a code-like token, redacted message.
lib.errInfo = (where, status, body) => {
  const b = lib.parse(body);
  let code = '';
  let msg = '';
  if (b && typeof b === 'object') {
    if (typeof b.code === 'string' && /^[A-Za-z_]{2,40}$/.test(b.code)) { code = b.code; }
    if (!code && typeof b.error === 'string' && /^[A-Za-z_]{2,40}$/.test(b.error)) { code = b.error; }
    const m = b.message !== undefined ? b.message : (b.error_description !== undefined ? b.error_description : '');
    msg = typeof m === 'string' ? m : JSON.stringify(m);
  }
  const st = Number(status);
  return { where: String(where), status: isFinite(st) ? st : 0, code: code, message: lib.redact(msg) };
};
lib.ok2xx = (status) => { const s = Number(status); return s >= 200 && s <= 299; };

// ---- run state ------------------------------------------------------------
// fsState is small on purpose: counts, timings and ID-to-key maps only. Flow
// bodies and translations live in fsFrags so that most steps never carry them.
lib.STAGES = ['calibrate', 'auth', 'inventory', 'fetch', 'translate', 'gitlab'];
lib.state = (v) => {
  const s = lib.obj(v);
  if (!s.stages) { s.stages = {}; }
  if (!s.errors) { s.errors = []; }
  if (!s.notes) { s.notes = []; }
  if (!s.totals) { s.totals = { httpCalls: 0, httpMs: 0, bytesIn: 0, bytesOut: 0, fnRuns: 0, fnMs: 0, maxFnMs: 0, maxCallMs: 0, maxResponseBytes: 0, maxStateBytes: 0, maxFragsBytes: 0, handbacks: 0, handbackMs: 0, segments: 0, maxSegmentMs: 0 }; }
  return s;
};
lib.stage = (s, name) => {
  if (!s.stages[name]) {
    s.stages[name] = { status: 'pending', startTs: 0, endTs: 0, calls: 0, callMs: 0, maxCallMs: 0, fnMs: 0, maxFnMs: 0, bytesIn: 0, bytesOut: 0, items: 0, errors: 0 };
  }
  return s.stages[name];
};
lib.begin = (s, name, t) => {
  const st = lib.stage(s, name);
  if (!st.startTs) { st.startTs = t || lib.now(); }
  st.status = 'running';
  return st;
};
lib.end = (s, name, status, t) => {
  const st = lib.stage(s, name);
  if (!st.startTs) { st.startTs = t || lib.now(); }
  st.endTs = t || lib.now();
  st.status = status || 'ok';
  return st;
};
// Called first in every function: t0 is now, since is the time spent in the
// nodes between the previous function and this one (saves, evaluators, a REST
// call or a browser round trip).
lib.enter = (s) => {
  const t0 = lib.now();
  const since = s.lastTs ? Math.max(0, t0 - s.lastTs) : 0;
  return { t0: t0, since: since };
};
// Called last: charges the function's own run time to a stage.
lib.leave = (s, stageName, t0) => {
  const t = lib.now();
  const ms = Math.max(0, t - t0);
  const tot = s.totals;
  tot.fnRuns += 1;
  tot.fnMs += ms;
  tot.maxFnMs = Math.max(tot.maxFnMs, ms);
  if (stageName) {
    const st = lib.stage(s, stageName);
    st.fnMs += ms;
    st.maxFnMs = Math.max(st.maxFnMs, ms);
  }
  if (s.segStart) { tot.maxSegmentMs = Math.max(tot.maxSegmentMs, t - s.segStart); }
  s.lastTs = t;
  return s;
};
// A REST call measured from the end of the previous function to the start of this one.
lib.call = (s, stageName, ms, status, bytesIn, bytesOut) => {
  const st = lib.stage(s, stageName);
  const tot = s.totals;
  st.calls += 1;
  st.callMs += ms;
  st.maxCallMs = Math.max(st.maxCallMs, ms);
  st.bytesIn += bytesIn || 0;
  st.bytesOut += bytesOut || 0;
  tot.httpCalls += 1;
  tot.httpMs += ms;
  tot.maxCallMs = Math.max(tot.maxCallMs, ms);
  tot.bytesIn += bytesIn || 0;
  tot.bytesOut += bytesOut || 0;
  tot.maxResponseBytes = Math.max(tot.maxResponseBytes, bytesIn || 0);
  s.callsInSegment = (s.callsInSegment || 0) + 1;
};
lib.fail = (s, stageName, info) => {
  lib.stage(s, stageName).errors += 1;
  if (s.errors.length < 25) { s.errors.push(info); }
};
lib.note = (s, text) => { if (s.notes.length < 25 && s.notes.indexOf(text) < 0) { s.notes.push(String(text)); } };
// A new browser request began (the start page or a working page was submitted).
lib.segment = (s, t) => {
  s.segStart = t;
  s.callsInSegment = 0;
  s.totals.segments += 1;
};
// Hand back to the browser after handBackEvery calls (0 = never).
lib.shouldPause = (s) => s.handBackEvery > 0 && (s.callsInSegment || 0) >= s.handBackEvery;
lib.track = (s, frags) => {
  s.totals.maxStateBytes = Math.max(s.totals.maxStateBytes, lib.size(s));
  if (frags !== undefined) { s.totals.maxFragsBytes = Math.max(s.totals.maxFragsBytes, lib.size(frags)); }
};

// ---- URLs -------------------------------------------------------------------
lib.API_ORIGIN = /^https:\/\/api\.pingone\.[a-z]{2,6}(\.[a-z]{2,3})?$/;
lib.AUTH_ORIGIN = /^https:\/\/auth\.pingone\.[a-z]{2,6}(\.[a-z]{2,3})?$/;
lib.ENV_ID = /^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$/;
lib.SAFE_SEG = /^[A-Za-z0-9_-]{1,64}$/;
lib.GITLAB_API = /^https:\/\/[A-Za-z0-9.-]{1,253}(:[0-9]{1,5})?(\/[A-Za-z0-9._~-]{1,64})*\/api\/v4$/;
// A path template such as /v1/environments/{envId}/flows/{flowId}; nothing else may be substituted.
lib.fillPath = (tpl, vals) => {
  const t = lib.str(tpl);
  if (!/^\/v1\/[A-Za-z0-9_{}\/-]{1,200}$/.test(t) || t.indexOf('..') >= 0 || t.indexOf('//') >= 0) { throw new Error('Endpoint path setting is not a /v1/... path.'); }
  return t.replace(/\{([A-Za-z]+)\}/g, (m, k) => {
    const v = vals[k];
    if (v === undefined || !lib.SAFE_SEG.test(String(v))) { throw new Error('Endpoint path uses an unknown or unsafe {' + k + '} value.'); }
    return String(v);
  });
};
// Split an absolute URL into origin, path and query without relying on URL().
lib.splitUrl = (u) => {
  const m = /^(https:\/\/[^\/?#]+)(\/[^?#]*)?(\?[^#]*)?$/.exec(String(u || ''));
  return m ? { origin: m[1], path: m[2] || '/', query: m[3] || '' } : null;
};
lib.nextLink = (body) => {
  const l = body && body._links && body._links.next;
  const h = l && (typeof l === 'string' ? l : l.href);
  return typeof h === 'string' && h ? h : '';
};
lib.encSeg = (s) => encodeURIComponent(String(s));
// The three inventory collections, read in this order (stage 2).
lib.KINDS = [
  { name: 'flows', path: 'pathFlows', query: 'flowsListQuery', key: 'flows' },
  { name: 'connectorInstances', path: 'pathConnectorInstances', query: '', key: 'connectorInstances' },
  { name: 'variables', path: 'pathVariables', query: '', key: 'variables' }
];
lib.inventoryUrl = (s, k) => {
  const kind = lib.KINDS[k];
  const q = kind.query ? s.cfg[kind.query] : '';
  return s.cfg.apiOrigin + lib.fillPath(s.cfg[kind.path], { envId: s.cfg.sourceEnvironmentId }) + (q ? '?' + q : '');
};
lib.flowUrl = (s, id) => s.cfg.apiOrigin + lib.fillPath(s.cfg.pathFlow, { envId: s.cfg.sourceEnvironmentId, flowId: id });
lib.gitlabUrl = (s, rest) => s.cfg.gitlabApi + '/projects/' + lib.encSeg(s.cfg.gitlabProjectId) + rest;
lib.out = (s, action, progress, extra) => {
  const o = { state: s, action: action, url: s.url || '', progress: progress || '' };
  for (const k of Object.keys(extra || {})) { o[k] = extra[k]; }
  return o;
};

// ---- settings (flow variables the operator sets after import) ----------------
// Returns the checked configuration and a list of problems; never the values.
lib.SETTINGS = ['apiOrigin', 'authOrigin', 'workerEnvironmentId', 'sourceEnvironmentId', 'pathFlows', 'flowsListQuery', 'pathFlow',
  'pathConnectorInstances', 'pathVariables', 'gitlabApi', 'gitlabProjectId', 'flowTimeoutSeconds', 'httpTimeoutSeconds', 'calibrationLoops'];
lib.OPTIONAL = ['flowsListQuery', 'gitlabApi', 'gitlabProjectId', 'flowTimeoutSeconds', 'httpTimeoutSeconds', 'calibrationLoops'];
lib.checkSettings = (p) => {
  const cfg = {};
  const problems = [];
  const need = (name, re, msg, optional) => {
    const v = lib.str(p[name]);
    if (!v) { if (!optional) { problems.push({ name: name, issue: 'not set' }); } return ''; }
    if (!re.test(v)) { problems.push({ name: name, issue: msg }); return ''; }
    return v;
  };
  cfg.apiOrigin = need('apiOrigin', lib.API_ORIGIN, 'must be https://api.pingone.<tld>');
  cfg.authOrigin = need('authOrigin', lib.AUTH_ORIGIN, 'must be https://auth.pingone.<tld>');
  cfg.workerEnvironmentId = need('workerEnvironmentId', lib.ENV_ID, 'must be an environment ID (UUID)');
  cfg.sourceEnvironmentId = need('sourceEnvironmentId', lib.ENV_ID, 'must be an environment ID (UUID)');
  for (const [name, vals] of [['pathFlows', { envId: 'x' }], ['pathFlow', { envId: 'x', flowId: 'x' }], ['pathConnectorInstances', { envId: 'x' }], ['pathVariables', { envId: 'x' }]]) {
    const v = lib.str(p[name]);
    try { lib.fillPath(v, vals); cfg[name] = v; } catch (e) { problems.push({ name: name, issue: v ? 'must be a /v1/... path using only {envId}' + (name === 'pathFlow' ? ' and {flowId}' : '') : 'not set' }); }
  }
  cfg.flowsListQuery = need('flowsListQuery', /^[A-Za-z0-9_.,=&%-]{0,200}$/, 'must be a plain query string such as attributes=id,name', true);
  cfg.gitlabApi = need('gitlabApi', lib.GITLAB_API, 'must be https://<host>/api/v4', true);
  cfg.gitlabProjectId = need('gitlabProjectId', /^([0-9]{1,12}|[A-Za-z0-9_.-]{1,100}(\/[A-Za-z0-9_.-]{1,100}){1,5})$/, 'must be a numeric project ID or group/project path', true);
  cfg.flowTimeoutSeconds = lib.int(p.flowTimeoutSeconds, 300, 1, 86400);
  cfg.httpTimeoutSeconds = lib.int(p.httpTimeoutSeconds, 15, 1, 3600);
  cfg.calibrationLoops = lib.int(p.calibrationLoops, 10, 0, 50);
  return { cfg: cfg, problems: problems };
};
lib.varName = (n) => 'fs' + n.charAt(0).toUpperCase() + n.slice(1);

// ---- GitLab branch guard -------------------------------------------------------
// The flow writes only to a new, operator-named branch. Obvious long-lived or
// protected names are refused here; the default branch, existing branches and
// protected-branch patterns are refused again against GitLab itself.
lib.PROTECTED_NAMES = ['main', 'master', 'develop', 'development', 'dev', 'trunk', 'production', 'prod', 'staging', 'stage', 'test',
  'qa', 'uat', 'release', 'releases', 'stable', 'default', 'head', 'gh-pages', 'live'];
lib.PROTECTED_PREFIXES = ['release/', 'releases/', 'hotfix/', 'hotfixes/', 'prod/', 'production/', 'main/', 'master/', 'stable/', 'refs/'];
lib.checkBranch = (b) => {
  const s = String(b || '');
  if (!/^[A-Za-z0-9][A-Za-z0-9._\/-]{0,99}$/.test(s) || s.indexOf('..') >= 0 || s.indexOf('//') >= 0 || /[\/.]$/.test(s) ||
      /\.lock(\/|$)/.test(s) || s.indexOf('/.') >= 0) {
    return 'Branch name must be 1-100 characters of letters, digits, . _ - and /, without .., //, a trailing / or . or .lock.';
  }
  const low = s.toLowerCase();
  if (lib.PROTECTED_NAMES.indexOf(low) >= 0 || lib.PROTECTED_PREFIXES.some((x) => low.indexOf(x) === 0)) {
    return 'Branch name looks like a default or protected branch; use a new name such as davinci-feasibility/run-1.';
  }
  return '';
};
// GitLab protected-branch names may use * wildcards.
lib.matchesPattern = (pattern, name) => {
  const re = new RegExp('^' + String(pattern).split('*').map((x) => x.replace(/[.+?^${}()|[\]\\]/g, '\\$&')).join('.*') + '$');
  return re.test(name);
};
