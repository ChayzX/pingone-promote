// THE CONTRACT with the GitLab request job (embedded in every function as
// `contract`). It follows the final backend: internal/request (request.go,
// result.go), docs/gitlab-ci-integration.md and ADR 0017: requestVersion 1,
// resultVersion 1. Every file path, job name, field name, rule and value the
// app writes to or reads from GitLab for a request is defined here and
// nowhere else.
const contract = {};

// ---- where the request lives -------------------------------------------------------
contract.REQUEST_VERSION = 1;
contract.RESULT_VERSION = 1;
contract.BRANCH_PREFIX = 'pingone-promote/';
contract.REQUEST_FILE = '.pingone-promote/request.json';
contract.REQUEST_JOB = 'pingone-promote:request';
contract.RESULT_ARTIFACT = 'result.json';
contract.MODES = ['mr', 'branch'];
contract.CHOICES = ['update', 'keep'];

// ---- validation rules, as request.go applies them ---------------------------------------
contract.RE = {
  requestId: /^[A-Za-z0-9][A-Za-z0-9_-]{2,63}$/,
  env: /^[A-Za-z0-9][A-Za-z0-9_-]{0,62}$/,
  ident: /^[A-Za-z_][A-Za-z0-9_]{0,127}$/,
  secretName: /^[A-Za-z][A-Za-z0-9_]{0,127}$/,
  address: /^pingone_[a-z0-9_]+\.[A-Za-z0-9_\-]{1,512}$/,
  davinciId: /^[0-9a-fA-F]{32}$|^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$/,
  sha: /^[0-9a-f]{7,64}$/,
  label: /^[A-Za-z0-9_\-]{1,512}$/
};
contract.LIMITS = { refs: 20, refName: 200, requestedBy: 128, title: 255, commitMessage: 2000, description: 65536, size: 262144 };

// requestId: sortable, branch-safe, no tenant data. rnd is a base-36 string.
contract.newRequestId = (t, rnd) => {
  const d = new Date(t).toISOString().replace(/[-:]/g, '').replace('T', '-').slice(0, 15);
  return d + '-' + String(rnd).replace(/[^a-z0-9]/g, '').slice(0, 6).padEnd(6, '0');
};
contract.branchFor = (requestId) => contract.BRANCH_PREFIX + requestId;
contract.isRequestBranch = (b) => {
  const s = String(b || '');
  return s.indexOf(contract.BRANCH_PREFIX) === 0 && contract.RE.requestId.test(s.slice(contract.BRANCH_PREFIX.length));
};

// ---- request.json -------------------------------------------------------------------
// d: { requestId, requestedBy, source, flow:{id,name}, options:{deps, env, secrets, remove, inline, mode, title,
//      description, commitMessage}, dryRun }. Optional fields are sent only when they hold something
// (the job refuses unknown fields; empty optional ones are simply left out).
contract.buildRequest = (d) => {
  const o = d.options;
  const flow = contract.RE.davinciId.test(d.flow.id) ? { id: d.flow.id, name: d.flow.name } : { name: d.flow.name };
  const r = { requestVersion: contract.REQUEST_VERSION, requestId: d.requestId, requestedBy: d.requestedBy, source: d.source, flows: [flow] };
  const put = (k, v) => { if (v && (Array.isArray(v) ? v.length : Object.keys(v).length)) { r[k] = v; } };
  put('dependencyChoices', o.deps);
  put('envValues', o.env);
  put('secretMappings', o.secrets);
  put('inlineValues', o.inline);
  put('remove', o.remove);
  r.mode = o.mode;
  r.branchName = contract.branchFor(d.requestId);
  if (o.commitMessage) { r.commitMessage = o.commitMessage; }
  if (o.title) { r.title = o.title; }
  if (o.description) { r.description = o.description; }
  r.dryRun = !!d.dryRun;
  return r;
};
// The same checks the job makes, so a person never waits for a pipeline to
// learn about avoidable input. Returns [{field, message}] using the job's
// field paths. cfg: {rollout:[...]} from the app settings.
contract.validateRequest = (r, cfg) => {
  const out = [];
  const bad = (field, message) => out.push({ field: field, message: message });
  const printable = (s, max) => typeof s === 'string' && lib.utf8(s).length <= max && !/[\u0000-\u0008\u000b-\u001f]/.test(s);
  if (!contract.RE.requestId.test(r.requestId)) { bad('requestId', 'must match ' + contract.RE.requestId.source); }
  if (r.branchName !== contract.branchFor(r.requestId)) { bad('branchName', 'must be ' + contract.branchFor(r.requestId)); }
  if (!printable(r.requestedBy, contract.LIMITS.requestedBy) || /[\n\t]/.test(r.requestedBy)) { bad('requestedBy', 'must be one line of at most 128 characters'); }
  if (!contract.RE.env.test(r.source)) { bad('source', 'must be an environment name'); }
  if (cfg.rollout.indexOf(r.source) >= 0) { bad('source', 'must not be a rollout environment'); }
  if (!r.flows.length || r.flows.length > contract.LIMITS.refs) { bad('flows', 'choose 1 to 20 flows'); }
  r.flows.forEach((f, i) => {
    if (f.id && !contract.RE.davinciId.test(f.id)) { bad('flows[' + i + '].id', 'must be a DaVinci ID'); }
    if (f.name !== undefined && (!printable(f.name, contract.LIMITS.refName) || !f.name.trim())) { bad('flows[' + i + '].name', 'must be at most 200 printable characters'); }
  });
  for (const a of Object.keys(r.dependencyChoices || {})) {
    if (!contract.RE.address.test(a)) { bad('dependencyChoices', a + ' is not a resource address'); }
    if (contract.CHOICES.indexOf(r.dependencyChoices[a]) < 0) { bad('dependencyChoices.' + a, 'must be update or keep'); }
  }
  const envs = [r.source].concat(cfg.rollout);
  for (const env of Object.keys(r.envValues || {})) {
    if (!contract.RE.env.test(env) || envs.indexOf(env) < 0) { bad('envValues.' + env, 'may only name the source or a rollout environment'); continue; }
    for (const k of Object.keys(r.envValues[env])) {
      const v = r.envValues[env][k];
      if (!contract.RE.ident.test(k) || k === 'import_ids') { bad('envValues.' + env + '.' + k, 'must be a root variable name (import_ids is set by adoption, not here)'); }
      if (v === null || v === undefined || v === '') { bad('envValues.' + env + '.' + k, 'must not be empty'); }
      if (lib.looksSecret(JSON.stringify(v))) { bad('envValues.' + env + '.' + k, 'looks like a secret; secrets come from customer_secrets.yml'); }
    }
  }
  for (const k of Object.keys(r.secretMappings || {})) {
    if (!contract.RE.ident.test(k) || !contract.RE.secretName.test(r.secretMappings[k])) { bad('secretMappings.' + k, 'keys and values must be variable names'); }
  }
  for (const v of r.inlineValues || []) { if (!contract.RE.ident.test(v)) { bad('inlineValues', v + ' is not a variable name'); } }
  for (const a of r.remove || []) { if (!contract.RE.address.test(a)) { bad('remove', a + ' is not a resource address'); } }
  if (r.baseCommit !== undefined && !contract.RE.sha.test(r.baseCommit)) { bad('baseCommit', 'must be a commit SHA'); }
  if (contract.MODES.indexOf(r.mode) < 0) { bad('mode', 'must be mr or branch'); }
  if (r.commitMessage !== undefined && !printable(r.commitMessage.replace(/[\n\t]/g, ' '), contract.LIMITS.commitMessage)) { bad('commitMessage', 'must be at most 2000 characters'); }
  if (r.title !== undefined && (!printable(r.title, contract.LIMITS.title) || /\n/.test(r.title))) { bad('title', 'must be one line of at most 255 characters'); }
  if (r.description !== undefined && !printable(r.description.replace(/[\n\t]/g, ' '), contract.LIMITS.description)) { bad('description', 'must be at most 64 KiB'); }
  if (lib.utf8(JSON.stringify(r)).length > contract.LIMITS.size) { bad('request', 'is larger than 256 KiB'); }
  return out;
};
// Who asked, in git history as well as in request.json (the commit author is
// the project token's bot). Never the job's PingOne-Promote-Request trailer:
// that one marks the job's own commit (already_submitted).
contract.commitMessage = (req) => 'pingone-promote: ' + (req.dryRun ? 'preview' : 'submit') + ' request ' + req.requestId +
  '\n\nRequested-by: ' + req.requestedBy + '\nRequest-id: ' + req.requestId + '\n';
// The ONE Commits API body per Preview or Submit. The first commit creates the
// branch from the default branch; later ones update the file and pass the
// last request commit for optimistic concurrency.
contract.commitBody = (req, defaultBranch, lastCommitId) => {
  const action = { action: lastCommitId ? 'update' : 'create', file_path: contract.REQUEST_FILE, encoding: 'base64',
    content: lib.b64(JSON.stringify(req, null, 2) + '\n') };
  if (lastCommitId) { action.last_commit_id = lastCommitId; }
  const body = { branch: contract.branchFor(req.requestId), commit_message: contract.commitMessage(req), actions: [action] };
  if (!lastCommitId) { body.start_branch = defaultBranch; }
  return body;
};
// The lines the job copies into the MR description, read back by the Requests list.
contract.footerOf = (text) => {
  const t = String(text || '');
  const by = /Requested-by:\s*([^\n\r]{1,128})/.exec(t);
  const id = /Request-id:\s*([A-Za-z0-9][A-Za-z0-9_-]{2,63})/.exec(t);
  return { requestedBy: by ? by[1].trim() : '', requestId: id ? id[1] : '' };
};

// ---- result.json --------------------------------------------------------------------
contract.STATUS = {
  previewed: 'ok', submitted: 'ok', already_submitted: 'ok', no_changes: 'none', blocked: 'blocked', failed: 'failed'
};
contract.ACTIONS = {
  added: { sym: '+', label: 'New', cls: 'add' },
  updated: { sym: '~', label: 'Changed in source', cls: 'chg' },
  kept_differs: { sym: '!', label: 'Differs; Git version kept', cls: 'warn' },
  unchanged: { sym: '=', label: 'Matches Git', cls: 'same' },
  removed: { sym: '−', label: 'No longer managed (not destroyed)', cls: 'warn' },
  moved: { sym: '→', label: 'Moved (new address)', cls: 'chg' }
};
contract.ROW_STATUS = {
  'new': 'new', 'changed-in-source': 'changed in source', matches: 'matches Git', 'git-newer': 'Git is newer', 'changed-in-both': 'changed in both'
};
contract.ACTION_ORDER = ['added', 'updated', 'kept_differs', 'moved', 'removed', 'unknown', 'unchanged'];
contract.ERROR_CODES = ['conflict', 'preflight', 'partial', 'blocked'];
contract.ERROR_TYPES = ['validation', 'export', 'merge', 'gitlab', 'internal'];
contract.REQUEST_BLOCKERS = '_request';
contract.BLOCKER_TEXT = {
  missing_value: 'missing environment value', missing_overlay: 'environment file missing', undeclared_key: 'value the configuration does not declare',
  secret_in_overlay: 'secret in an environment file', invalid_import_key: 'invalid import key', changed_in_both: 'changed in both Git and the source',
  customer_secrets: 'customer_secrets.yml problem', secret_finding: 'possible secret in the change', missing_dependency: 'missing dependency',
  auto_tfvars: '*.auto.tfvars file present'
};
// Normalise result.json into what the screens use. The diff text is dropped
// (it can be 1 MiB; the screens link to diffUrl). Unknown shapes stay visible
// as problems instead of being dropped.
contract.normalizeResult = (raw) => {
  const r = lib.obj(raw);
  const s = (v, max) => lib.str(v).slice(0, max || 300);
  const out = { status: s(r.status, 40), mrUrl: s(r.mrUrl, 500), mrIid: lib.NUM.test(String(r.mrIid)) && Number(r.mrIid) > 0 ? String(r.mrIid) : '', branchUrl: s(r.branchUrl, 500),
    diffUrl: s(r.diffUrl, 500), commitId: lib.SHA.test(lib.str(r.commitId)) ? lib.str(r.commitId) : '', changes: [], summary: {}, requiredSecrets: [],
    blockers: {}, warnings: [], selected: [], error: null, problems: [] };
  if (!Object.keys(r).length) { out.problems.push('result.json is empty or not JSON'); return out; }
  if (r.resultVersion !== contract.RESULT_VERSION) { out.problems.push('unexpected resultVersion ' + lib.redact(r.resultVersion, 20)); }
  if (!lib.has(contract.STATUS, out.status)) { out.problems.push('unknown status ' + lib.redact(out.status, 40)); }
  for (const c of lib.arr(r.changes).slice(0, 2000)) {
    const address = s(c && c.address, 600);
    if (!address) { out.problems.push('a change without an address'); continue; }
    const action = lib.has(contract.ACTIONS, c.action) ? c.action : 'unknown';
    if (action === 'unknown') { out.problems.push('unknown change action ' + lib.redact(c.action, 40)); }
    out.changes.push({ address: address, action: action, role: c.role === 'selected' ? 'selected' : (c.role === 'dependency' ? 'dependency' : ''),
      status: lib.has(contract.ROW_STATUS, c.status) ? c.status : '', file: s(c.file, 200), from: s(c.from, 600), reason: lib.redact(c.reason, 300) });
  }
  const sum = lib.obj(r.summary);
  for (const a of Object.keys(contract.ACTIONS)) { out.summary[a] = Number(sum[a]) || out.changes.filter((c) => c.action === a).length; }
  for (const x of lib.arr(r.requiredSecrets)) {
    const key = lib.str(x && x.key);
    if (!key) { continue; }
    out.requiredSecrets.push({ key: key.slice(0, 128), varName: s(x.varName, 128), field: s(x.field, 128), declared: x.declared === true, vault: lib.redact(x.vault, 300) });
  }
  const bl = lib.obj(r.blockers);
  for (const env of Object.keys(bl)) {
    const list = lib.arr(bl[env]).map((b) => (typeof b === 'string' ? { code: '', var: '', address: '', message: lib.redact(b, 300) }
      : { code: s(b && b.code, 40), var: s(b && b.var, 128), address: s(b && b.address, 600), message: lib.redact(b && b.message, 300) }));
    if (list.length) { out.blockers[env] = list; }
  }
  out.warnings = lib.arr(r.warnings).slice(0, 20).map((w) => lib.redact(w, 300));
  out.selected = lib.arr(r.selected).slice(0, 20).map((x) => ({ name: s(x && x.name, 200), address: s(x && x.address, 600) }));
  if (r.error && typeof r.error === 'object') {
    const e = r.error;
    out.error = { code: contract.ERROR_CODES.indexOf(e.code) >= 0 ? e.code : 'unknown', type: contract.ERROR_TYPES.indexOf(e.type) >= 0 ? e.type : '',
      message: lib.redact(e.message, 400), retryable: e.retryable === true, field: s(e.field, 200),
      candidates: lib.arr(e.candidates).slice(0, 20).map((x) => ({ name: s(x && x.name, 200), address: s(x && x.address, 600) })) };
  } else if (out.status === 'blocked') {
    out.error = { code: 'blocked', type: 'validation', message: 'Readiness: some environments are missing values.', retryable: false, field: '', candidates: [] };
  } else if (out.status === 'failed') {
    out.error = { code: 'unknown', type: '', message: 'The request job failed without saying why.', retryable: false, field: '', candidates: [] };
  }
  return out;
};
contract.mrIid = (url) => { const m = /\/-\/merge_requests\/([0-9]{1,12})(?:[\/?#]|$)/.exec(String(url || '')); return m ? m[1] : ''; };

// ---- typed errors: what each means for the person ---------------------------------
contract.ERROR_TEXT = {
  conflict: { title: 'GitLab changed while this request was open', fix: 'Something else changed the repository or this request branch. Review the options and preview again.' },
  preflight: { title: 'The request did not pass the checks', fix: 'Fix the problem below, then preview again.' },
  partial: { title: 'The request was only partly written', fix: 'Part of the change is already on the request branch. Open it in GitLab before trying again.' },
  blocked: { title: 'Readiness: some environments are missing values', fix: 'Fill in the missing values below, then preview again.' },
  unknown: { title: 'The request job reported an error', fix: 'Open the job in GitLab for details.' }
};
contract.TYPE_TEXT = { validation: 'the request itself', export: 'reading the source env', merge: 'merging into Git', gitlab: 'talking to GitLab', internal: 'the request job' };
