// THE CONTRACT with the GitLab request job (embedded in every function as
// `contract`). It is PROVISIONAL: the backend (branch terraform/v2-engine) is
// not final. Every file path, job name, field name and value the app writes
// to or reads from GitLab for a request is defined here and nowhere else.
const contract = {};

// ---- where the request lives -------------------------------------------------------
contract.REQUEST_VERSION = 1;
contract.BRANCH_PREFIX = 'pingone-promote/';
contract.REQUEST_FILE = '.pingone-promote/request.json';
contract.REQUEST_JOB = 'pingone-promote:request';
contract.RESULT_ARTIFACT = 'result.json';
contract.MODES = ['mr', 'branch'];
contract.CHOICES = ['update', 'keep'];

// requestId: sortable, branch-safe, no tenant data. rnd is a base-36 string.
contract.newRequestId = (t, rnd) => {
  const d = new Date(t).toISOString().replace(/[-:]/g, '').replace('T', '-').slice(0, 15);
  return d + '-' + String(rnd).replace(/[^a-z0-9]/g, '').slice(0, 6).padEnd(6, '0');
};
contract.REQUEST_ID = /^[0-9]{8}-[0-9]{6}-[a-z0-9]{6}$/;
contract.branchFor = (requestId) => contract.BRANCH_PREFIX + requestId;
contract.isRequestBranch = (b) => {
  const s = String(b || '');
  return s.indexOf(contract.BRANCH_PREFIX) === 0 && contract.REQUEST_ID.test(s.slice(contract.BRANCH_PREFIX.length));
};

// ---- request.json -------------------------------------------------------------------
// d: { requestId, requestedBy, source, flow:{id,name}, options:{deps, env, secrets, mode, title, description, commitMessage}, dryRun }
contract.buildRequest = (d) => ({
  requestVersion: contract.REQUEST_VERSION,
  requestId: d.requestId,
  requestedBy: d.requestedBy,
  source: d.source,
  flows: [{ id: d.flow.id, name: d.flow.name }],
  dependencyChoices: d.options.deps || {},
  envValues: d.options.env || {},
  secretMappings: d.options.secrets || {},
  mode: d.options.mode,
  branchName: contract.branchFor(d.requestId),
  commitMessage: d.options.commitMessage,
  title: d.options.title,
  description: d.options.description,
  dryRun: !!d.dryRun
});
// Who asked, in git history as well as in request.json (the commit author is the project token's bot).
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
// Lines the backend copies into the MR description, read back by the Requests list.
contract.footerOf = (text) => {
  const t = String(text || '');
  const by = /Requested-by:\s*([^\n\r]{1,120})/.exec(t);
  const id = /Request-id:\s*([0-9]{8}-[0-9]{6}-[a-z0-9]{6})/.exec(t);
  return { requestedBy: by ? by[1].trim() : '', requestId: id ? id[1] : '' };
};

// ---- result.json --------------------------------------------------------------------
contract.ACTIONS = {
  added: { sym: '+', label: 'New', cls: 'add' },
  updated: { sym: '~', label: 'Changed in source', cls: 'chg' },
  kept_differs: { sym: '!', label: 'Differs; Git version kept', cls: 'warn' },
  unchanged: { sym: '=', label: 'Matches Git', cls: 'same' }
};
contract.ERROR_CODES = ['conflict', 'preflight', 'partial', 'blocked'];
// Normalise result.json into what the screens use. Unknown shapes are kept
// visible as problems instead of being dropped.
contract.normalizeResult = (raw) => {
  const r = lib.obj(raw);
  const out = { status: lib.str(r.status), mrUrl: lib.str(r.mrUrl), branchUrl: lib.str(r.branchUrl), diffUrl: lib.str(r.diffUrl),
    changes: [], requiredSecrets: [], blockers: {}, error: null, problems: [] };
  if (!Object.keys(r).length) { out.problems.push('result.json is empty or not JSON'); return out; }
  for (const c of lib.arr(r.changes)) {
    const address = lib.str(c && c.address);
    if (!address) { out.problems.push('a change without an address'); continue; }
    const action = lib.has(contract.ACTIONS, c.action) ? c.action : 'unknown';
    if (action === 'unknown') { out.problems.push('unknown change action ' + lib.redact(c.action, 40)); }
    // role is a requested addition: "selected" for the chosen flow, "dependency" otherwise.
    out.changes.push({ address: address.slice(0, 300), action: action, role: c.role === 'selected' ? 'selected' : (c.role === 'dependency' ? 'dependency' : '') });
  }
  for (const x of lib.arr(r.requiredSecrets)) {
    if (typeof x === 'string') { out.requiredSecrets.push({ key: x.slice(0, 200), varName: '', field: '' }); continue; }
    const key = lib.str(x && (x.key || x.varName));
    if (key) { out.requiredSecrets.push({ key: key.slice(0, 200), varName: lib.str(x.varName).slice(0, 128), field: lib.str(x.field).slice(0, 128) }); }
  }
  const bl = lib.obj(r.blockers);
  for (const env of Object.keys(bl)) {
    out.blockers[env] = lib.arr(bl[env]).map((b) => (typeof b === 'string' ? { code: '', var: '', message: lib.redact(b, 300) }
      : { code: lib.str(b && b.code).slice(0, 40), var: lib.str(b && b.var).slice(0, 128), message: lib.redact(b && b.message, 300) }));
    if (!out.blockers[env].length) { delete out.blockers[env]; }
  }
  if (r.error && typeof r.error === 'object') {
    const code = contract.ERROR_CODES.indexOf(r.error.code) >= 0 ? r.error.code : 'unknown';
    out.error = { code: code, message: lib.redact(r.error.message, 400) };
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
