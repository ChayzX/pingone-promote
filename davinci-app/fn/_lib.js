// Shared helpers embedded in every app custom function (as `lib`).
// gen_app.py removes full-line comments and indentation when embedding, so
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
lib.arr = (v) => {
  const p = lib.parse(v);
  return Array.isArray(p) ? p : [];
};
lib.str = (v) => {
  if (v === undefined || v === null) { return ''; }
  const s = String(typeof v === 'object' ? JSON.stringify(v) : v).trim();
  // An unresolved {{reference}} means the value was never set.
  return /^\{\{[^}]*\}\}$/.test(s) ? '' : s;
};
lib.int = (v, dflt, min, max) => {
  const s = lib.str(v);
  if (!/^[0-9]{1,9}$/.test(s)) { return dflt; }
  return Math.min(max, Math.max(min, Number(s)));
};
lib.has = (o, k) => Object.prototype.hasOwnProperty.call(o, k);

// Text shown on a screen. Braces are escaped too, so text that came from a
// user or an API can never form a {{reference}} that DaVinci would resolve.
lib.esc = (s) => String(s === undefined || s === null ? '' : s)
  .replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;').replace(/'/g, '&#39;')
  .replace(/\{/g, '&#123;').replace(/\}/g, '&#125;');

// Free text from an API or GitLab job: remove anything that could carry a
// credential or identify the tenant, then cut it short.
lib.redact = (s, max) => String(s === undefined || s === null ? '' : s)
  .replace(/(bearer|basic)\s+[A-Za-z0-9._~+\/=-]+/gi, '$1 <redacted>')
  .replace(/gl[a-z]{2,5}-[A-Za-z0-9_\-]{8,}/g, '<redacted>')
  .replace(/eyJ[A-Za-z0-9_\-]+\.[A-Za-z0-9_\-]+\.[A-Za-z0-9_\-]*/g, '<redacted>')
  .replace(/[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}/g, '<id>')
  .replace(/(client_secret|password|token|secret)=[^&\s]+/gi, '$1=<redacted>')
  .replace(/[\u0000-\u001f\u007f]+/g, ' ')
  .slice(0, max || 300);
lib.ok2xx = (status) => { const s = Number(status); return s >= 200 && s <= 299; };
lib.httpMessage = (body) => {
  const b = lib.parse(body);
  if (b && typeof b === 'object') {
    const m = b.message !== undefined ? b.message : (b.error_description !== undefined ? b.error_description : b.error);
    return lib.redact(typeof m === 'string' ? m : JSON.stringify(m || ''), 200);
  }
  return typeof b === 'string' ? lib.redact(b, 200) : '';
};

// ---- base64 (UTF-8), without Buffer, btoa or TextEncoder ---------------------
lib.B64 = 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/';
lib.utf8 = (str) => {
  const out = [];
  const s = String(str);
  for (let i = 0; i < s.length; i++) {
    let c = s.charCodeAt(i);
    if (c >= 0xd800 && c <= 0xdbff && i + 1 < s.length) {
      const d = s.charCodeAt(i + 1);
      if (d >= 0xdc00 && d <= 0xdfff) { c = 0x10000 + ((c - 0xd800) << 10) + (d - 0xdc00); i++; }
    }
    if (c < 0x80) { out.push(c); } else if (c < 0x800) { out.push(0xc0 | (c >> 6), 0x80 | (c & 63)); } else if (c < 0x10000) {
      out.push(0xe0 | (c >> 12), 0x80 | ((c >> 6) & 63), 0x80 | (c & 63));
    } else { out.push(0xf0 | (c >> 18), 0x80 | ((c >> 12) & 63), 0x80 | ((c >> 6) & 63), 0x80 | (c & 63)); }
  }
  return out;
};
lib.b64 = (str) => {
  const b = lib.utf8(str);
  const A = lib.B64;
  let o = '';
  for (let i = 0; i < b.length; i += 3) {
    const x = b[i];
    const y = i + 1 < b.length ? b[i + 1] : 0;
    const z = i + 2 < b.length ? b[i + 2] : 0;
    o += A.charAt(x >> 2) + A.charAt(((x & 3) << 4) | (y >> 4));
    o += i + 1 < b.length ? A.charAt(((y & 15) << 2) | (z >> 6)) : '=';
    o += i + 2 < b.length ? A.charAt(z & 63) : '=';
  }
  return o;
};
lib.unb64 = (text) => {
  const t = String(text || '').replace(/[^A-Za-z0-9+\/]/g, '');
  const bytes = [];
  for (let i = 0; i + 1 < t.length; i += 4) {
    const n = [0, 1, 2, 3].map((k) => (i + k < t.length ? lib.B64.indexOf(t.charAt(i + k)) : -1));
    bytes.push((n[0] << 2) | (n[1] >> 4));
    if (n[2] >= 0) { bytes.push(((n[1] & 15) << 4) | (n[2] >> 2)); }
    if (n[3] >= 0) { bytes.push(((n[2] & 3) << 6) | n[3]); }
  }
  let s = '';
  for (let i = 0; i < bytes.length; i++) {
    const c = bytes[i];
    if (c < 0x80) { s += String.fromCharCode(c); } else if (c < 0xe0) { s += String.fromCharCode(((c & 31) << 6) | (bytes[++i] & 63)); } else if (c < 0xf0) {
      s += String.fromCharCode(((c & 15) << 12) | ((bytes[++i] & 63) << 6) | (bytes[++i] & 63));
    } else {
      const cp = ((c & 7) << 18) | ((bytes[++i] & 63) << 12) | ((bytes[++i] & 63) << 6) | (bytes[++i] & 63);
      s += String.fromCharCode(0xd800 + ((cp - 0x10000) >> 10), 0xdc00 + ((cp - 0x10000) & 1023));
    }
  }
  return s;
};

// ---- settings (flow variables the operator sets after import) ----------------
// Returns the checked configuration and a list of problems; never the values.
lib.API_ORIGIN = /^https:\/\/api\.pingone\.[a-z]{2,6}(\.[a-z]{2,3})?$/;
lib.AUTH_ORIGIN = /^https:\/\/auth\.pingone\.[a-z]{2,6}(\.[a-z]{2,3})?$/;
lib.ENV_ID = /^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$/;
lib.ENV_NAME = /^[a-z][a-z0-9_-]{0,30}$/;
lib.GITLAB_API = /^https:\/\/[A-Za-z0-9.-]{1,253}(:[0-9]{1,5})?(\/[A-Za-z0-9._~-]{1,64})*\/api\/v4$/;
lib.SETTINGS = ['apiOrigin', 'authOrigin', 'workerEnvironmentId', 'sourceEnvs', 'pathFlows', 'flowsListQuery', 'rolloutEnvs', 'gitlabApi',
  'gitlabProjectId', 'defaultBranch', 'applyJobPattern', 'environmentPattern', 'mrLabel', 'allowedGroups', 'runTimeoutSeconds',
  'pipelineAppearSeconds', 'statusRefreshSeconds', 'statusMaxMinutes', 'childPipelines'];
lib.varName = (n) => 'pp' + n.charAt(0).toUpperCase() + n.slice(1);
lib.checkSettings = (p) => {
  const cfg = {};
  const problems = [];
  const need = (name, re, msg, dflt) => {
    const v = lib.str(p[name]);
    if (!v) { if (dflt === undefined) { problems.push({ name: name, issue: 'not set' }); } return dflt || ''; }
    if (!re.test(v)) { problems.push({ name: name, issue: msg }); return ''; }
    return v;
  };
  cfg.apiOrigin = need('apiOrigin', lib.API_ORIGIN, 'must be https://api.pingone.<tld>');
  cfg.authOrigin = need('authOrigin', lib.AUTH_ORIGIN, 'must be https://auth.pingone.<tld>');
  cfg.workerEnvironmentId = need('workerEnvironmentId', lib.ENV_ID, 'must be an environment ID (UUID)');
  // sourceEnvs: "dev=<envId>, qa=<envId>": the envs a person may pick as the source.
  cfg.sources = {};
  const srcText = lib.str(p.sourceEnvs);
  for (const part of srcText.split(',').map((x) => x.trim()).filter((x) => x)) {
    const m = /^([a-z][a-z0-9_-]{0,30})=(.+)$/.exec(part);
    if (!m || !lib.ENV_ID.test(m[2].trim())) { problems.push({ name: 'sourceEnvs', issue: 'must be name=<environment ID> pairs separated by commas' }); break; }
    cfg.sources[m[1]] = m[2].trim();
  }
  if (!srcText) { problems.push({ name: 'sourceEnvs', issue: 'not set' }); }
  const path = lib.str(p.pathFlows);
  if (!/^\/v1\/[A-Za-z0-9_{}\/-]{1,200}$/.test(path) || path.indexOf('{envId}') < 0 || path.indexOf('..') >= 0 || /\{(?!envId\})/.test(path)) {
    problems.push({ name: 'pathFlows', issue: path ? 'must be a /v1/... path using only {envId}' : 'not set' });
  } else { cfg.pathFlows = path; }
  cfg.flowsListQuery = need('flowsListQuery', /^[A-Za-z0-9_.,=&%-]{0,200}$/, 'must be a plain query string such as attributes=id,name', '');
  cfg.rollout = lib.str(p.rolloutEnvs).split(',').map((x) => x.trim()).filter((x) => x);
  if (!cfg.rollout.length || cfg.rollout.some((x) => !lib.ENV_NAME.test(x))) { problems.push({ name: 'rolloutEnvs', issue: 'must be env names in rollout order, such as qa,stage,prod' }); }
  cfg.gitlabApi = need('gitlabApi', lib.GITLAB_API, 'must be https://<host>/api/v4');
  cfg.gitlabWeb = cfg.gitlabApi ? cfg.gitlabApi.replace(/\/api\/v4$/, '') : '';
  cfg.gitlabProjectId = need('gitlabProjectId', /^([0-9]{1,12}|[A-Za-z0-9_.-]{1,100}(\/[A-Za-z0-9_.-]{1,100}){1,5})$/, 'must be a numeric project ID or group/project path');
  cfg.defaultBranch = need('defaultBranch', /^[A-Za-z0-9][A-Za-z0-9._\/-]{0,99}$/, 'must be a branch name', 'main');
  cfg.applyJobPattern = need('applyJobPattern', /^[A-Za-z0-9 :._\/-]{0,80}\{env\}[A-Za-z0-9 :._\/-]{0,80}$/, 'must contain {env}, such as apply:{env}', 'apply:{env}');
  cfg.environmentPattern = need('environmentPattern', /^[A-Za-z0-9 :._\/-]{0,80}\{env\}[A-Za-z0-9 :._\/-]{0,80}$/, 'must contain {env}', '{env}');
  cfg.mrLabel = need('mrLabel', /^[A-Za-z0-9 :._-]{1,60}$/, 'must be a GitLab label name', 'pingone-promote');
  // Who may use the app: PingOne group names, or * for every user the sign-in subflow admits.
  const groups = lib.str(p.allowedGroups);
  cfg.allowedGroups = groups === '*' ? ['*'] : groups.split(',').map((x) => x.trim()).filter((x) => x);
  if (!cfg.allowedGroups.length) { problems.push({ name: 'allowedGroups', issue: 'not set (use PingOne group names, or * to admit every signed-in user)' }); }
  cfg.runTimeoutSeconds = lib.int(p.runTimeoutSeconds, 600, 60, 7200);
  cfg.pipelineAppearSeconds = lib.int(p.pipelineAppearSeconds, 120, 10, 3600);
  cfg.statusRefreshSeconds = lib.int(p.statusRefreshSeconds, 30, 10, 600);
  cfg.statusMaxMinutes = lib.int(p.statusMaxMinutes, 30, 1, 240);
  cfg.childPipelines = !/^(no|false|0|off)$/i.test(lib.str(p.childPipelines));
  return { cfg: cfg, problems: problems };
};

// ---- URLs ---------------------------------------------------------------------
lib.enc = (s) => encodeURIComponent(String(s));
lib.NUM = /^[0-9]{1,12}$/;
lib.SHA = /^[0-9a-f]{40}$/;
lib.flowsUrl = (cfg, source) => {
  const id = cfg.sources[source];
  if (!id) { throw new Error('unknown source env'); }
  return cfg.apiOrigin + cfg.pathFlows.replace('{envId}', id) + (cfg.flowsListQuery ? '?' + cfg.flowsListQuery : '');
};
lib.tokenUrl = (cfg) => cfg.authOrigin + '/' + cfg.workerEnvironmentId + '/as/token';
lib.nextLink = (body, origin) => {
  const l = body && body._links && body._links.next;
  const h = l && (typeof l === 'string' ? l : l.href);
  return typeof h === 'string' && h.indexOf(origin + '/') === 0 ? h : '';
};
// Every GitLab URL the app may call. Anything else throws, so no function can
// reach another endpoint, whatever is in the state.
lib.gl = (cfg, kind, a) => {
  a = a || {};
  const base = cfg.gitlabApi + '/projects/' + lib.enc(cfg.gitlabProjectId);
  const num = (v) => { if (!lib.NUM.test(String(v))) { throw new Error('bad id'); } return String(v); };
  const sha = (v) => { if (!lib.SHA.test(String(v))) { throw new Error('bad sha'); } return String(v); };
  const reqBranch = (b) => { if (!contract.isRequestBranch(b)) { throw new Error('not a request branch'); } return lib.enc(b); };
  switch (kind) {
    case 'commit': return { method: 'POST', url: base + '/repository/commits' };
    case 'deleteBranch': return { method: 'DELETE', url: base + '/repository/branches/' + reqBranch(a.branch) };
    case 'branch': return { method: 'GET', url: base + '/repository/branches/' + reqBranch(a.branch) };
    case 'requestFile': return { method: 'GET', url: base + '/repository/files/' + lib.enc(contract.REQUEST_FILE) + '?ref=' + reqBranch(a.branch) };
    case 'branchPipelines': return { method: 'GET', url: base + '/pipelines?ref=' + reqBranch(a.branch) + '&sha=' + sha(a.sha) + '&order_by=id&sort=desc&per_page=5' };
    case 'jobs': return { method: 'GET', url: base + '/pipelines/' + num(a.id) + '/jobs?per_page=100' };
    case 'bridges': return { method: 'GET', url: base + '/pipelines/' + num(a.id) + '/bridges?per_page=20' };
    case 'artifact': return { method: 'GET', url: base + '/jobs/' + num(a.id) + '/artifacts/' + contract.RESULT_ARTIFACT };
    case 'mr': return { method: 'GET', url: base + '/merge_requests/' + num(a.iid) };
    case 'mrsBySource': return { method: 'GET', url: base + '/merge_requests?source_branch=' + reqBranch(a.branch) + '&state=all&per_page=5' };
    case 'mrList': return { method: 'GET', url: base + '/merge_requests?labels=' + lib.enc(cfg.mrLabel) + '&state=all&order_by=updated_at&sort=desc&per_page=20' };
    case 'requestBranches': return { method: 'GET', url: base + '/repository/branches?search=' + lib.enc('^' + contract.BRANCH_PREFIX) + '&per_page=20' };
    case 'mainPipelines': return { method: 'GET', url: base + '/pipelines?ref=' + lib.enc(cfg.defaultBranch) + '&sha=' + sha(a.sha) + '&order_by=id&sort=asc&per_page=5' };
    case 'deployments': return { method: 'GET', url: base + '/deployments?environment=' + lib.enc(a.environment) + '&order_by=id&sort=desc&per_page=5' };
    case 'deployment': return { method: 'GET', url: base + '/deployments/' + num(a.id) };
    case 'mergeBase': return { method: 'GET', url: base + '/repository/merge_base?refs[]=' + sha(a.a) + '&refs[]=' + sha(a.b) };
    case 'commitMrs': return { method: 'GET', url: base + '/repository/commits/' + sha(a.sha) + '/merge_requests' };
    default: throw new Error('unknown GitLab call ' + kind);
  }
};
// A GitLab page link from an API response, shown only if it points at our GitLab.
lib.webUrl = (cfg, u) => {
  const s = typeof u === 'string' ? u : '';
  return cfg.gitlabWeb && s.indexOf(cfg.gitlabWeb + '/') === 0 && /^[A-Za-z0-9._~:\/?#@!$&'()*+,;=%-]{1,500}$/.test(s) ? s : '';
};

// ---- run state ------------------------------------------------------------------
lib.state = (v) => {
  const s = lib.obj(v);
  if (!s.notes) { s.notes = []; }
  return s;
};
lib.note = (s, kind, text) => { s.notes.push({ kind: kind, text: String(text) }); };
lib.out = (s, action, extra) => {
  s.action = action;
  const o = { state: s, action: action, url: s.url || '' };
  for (const k of Object.keys(extra || {})) { o[k] = extra[k]; }
  return o;
};
lib.iso = (t) => new Date(t).toISOString();
lib.secs = (ms) => Math.max(0, Math.round(ms / 1000));
lib.dur = (ms) => { const t = lib.secs(ms); return t < 60 ? t + 's' : Math.floor(t / 60) + 'm ' + (t % 60) + 's'; };
// Per browser request: GitLab reads stop at this many so one request stays fast.
lib.MAX_CALLS = 10;
// Values a person types must not be credentials; those belong in Vault.
lib.looksSecret = (v) => /(gl[a-z]{2,5}-[A-Za-z0-9_\-]{8,}|eyJ[A-Za-z0-9_\-]{8,}\.|-----BEGIN|(bearer|basic)\s+[A-Za-z0-9+\/=._-]{12,}|client_secret|password=)/i.test(String(v));

// ---- GitLab read queue ------------------------------------------------------------
// One browser request runs a short queue of GitLab GETs through one REST node:
// gl_read.js handles each answer, may add reads, and renders when it is empty.
lib.queue = (s, phase, items) => {
  s.q = { phase: phase, todo: items.slice(), calls: 0 };
  return lib.nextRead(s);
};
lib.nextRead = (s) => {
  const it = s.q.todo[0];
  const c = lib.gl(s.cfg, it.k, it);
  if (c.method !== 'GET') { throw new Error('the read queue only reads'); }
  s.url = c.url;
  return 'read';
};

// ---- flow search (#93): case-insensitive substring on the name, or an exact ID --------
lib.searchFlows = (items, query) => {
  const q = String(query || '').trim();
  const low = q.toLowerCase();
  const all = Array.isArray(items) ? items : [];
  const byId = all.filter((f) => f.id === q);
  const found = byId.length ? byId : all.filter((f) => String(f.name).toLowerCase().indexOf(low) >= 0);
  const k = (f) => String(f.name).toLowerCase() + '\u0000' + f.id;
  found.sort((a, b) => (k(a) < k(b) ? -1 : k(a) > k(b) ? 1 : 0));
  return { matches: found.slice(0, 50), total: found.length };
};

// ---- options, readiness and the #57 Submit rule ---------------------------------------
lib.optionsKey = (o) => JSON.stringify([o.mode, o.title, o.description, o.commitMessage, o.deps, o.env, o.secrets]);
lib.currentPreview = (s) => {
  // The preview for exactly the current options, if there is one.
  const key = s.req ? lib.optionsKey(s.req.options) : '';
  return s.preview && s.preview.key === key ? s.preview : null;
};
lib.blockerCount = (r) => Object.keys((r && r.blockers) || {}).length;
lib.onlyUnchanged = (r) => !!r && !r.error && r.changes.length > 0 && r.changes.every((c) => c.action === 'unchanged');
lib.canPreview = (s) => {
  if (!s.req) { return 'Choose a flow first.'; }
  if (s.req.gone) { return 'This request\'s branch no longer exists. Start a new request.'; }
  if (s.sub.state === 'running') { return 'A request is running.'; }
  if (s.sub.state === 'unknown') { return 'The outcome of the last submit is unknown. Check GitLab first.'; }
  if (s.sub.state === 'done') { return 'This request is already submitted.'; }
  if (Object.keys(s.errors || {}).length) { return 'Fix the highlighted fields first.'; }
  return '';
};
lib.canSubmit = (s) => {
  const p = lib.canPreview(s);
  if (p) { return p; }
  const key = lib.optionsKey(s.req.options);
  if (s.sub.state === 'error' && s.sub.key === key) { return 'The last submit failed with these exact options. Change them to submit again.'; }
  const cur = lib.currentPreview(s);
  if (cur && cur.result) {
    const r = cur.result;
    if (r.error) { return 'The preview of these options failed. Change them to submit.'; }
    if (lib.blockerCount(r)) { return 'Readiness: ' + lib.blockerCount(r) + ' environment(s) are missing values.'; }
    if (lib.onlyUnchanged(r)) { return 'Nothing to change: Git already matches the source for this flow.'; }
  }
  const secrets = (s.preview && s.preview.result && s.preview.result.requiredSecrets) || [];
  const confirmed = s.req.options.confirmed || {};
  if (secrets.some((x) => !confirmed[x.key])) { return 'Confirm that each required secret is in Vault for every environment.'; }
  return '';
};
