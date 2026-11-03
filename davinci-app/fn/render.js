// Build the HTML for the current screen from the state. The one App screen
// node shows it. All dynamic text goes through lib.esc.
const s = lib.state(params.state);
const cfg = s.cfg;
const e = lib.esc;
const now = lib.now();
let n = 0;
const uid = (p) => p + (++n);
const attr = (path) => ' data-pp="' + e(JSON.stringify(path)) + '"';
const button = (value, label, o) => {
  o = o || {};
  const why = o.why ? uid('why') : '';
  return '<button type="submit" class="pp-btn' + (o.ghost ? ' ghost' : '') + (o.danger ? ' danger' : '') + '" data-id="button" data-skcomponent="skbutton" data-skbuttontype="form-submit" data-skform="ppMain" data-skbuttonvalue="' + e(value) + '"' +
    (o.why ? ' disabled aria-disabled="true" aria-describedby="' + why + '"' + (o.onEdit ? ' data-pp-enable-on-edit="' + why + '"' : '') : '') + '>' + e(label) + '</button>' +
    (o.why ? '<span class="pp-why" id="' + why + '">' + e(o.why) + (o.onEdit ? ' Changing an option turns Submit back on.' : '') + '</span>' : '');
};
const link = (url, text) => (url ? '<a href="' + e(url) + '" rel="noopener noreferrer" target="_blank">' + e(text) + '<span class="pp-sr"> (opens GitLab in a new tab)</span></a>' : '');
const field = (id, label, input, err, hint) => '<div class="pp-field' + (err ? ' has-err' : '') + '"><label for="' + id + '">' + e(label) + '</label>' +
  (hint ? '<p class="pp-hint" id="' + id + '-hint">' + e(hint) + '</p>' : '') + (err ? '<p class="pp-err" id="' + id + '-err">' + e(err) + '</p>' : '') + input + '</div>';
const described = (id, err, hint) => {
  const ids = [hint ? id + '-hint' : '', err ? id + '-err' : ''].filter((x) => x).join(' ');
  return (ids ? ' aria-describedby="' + ids + '"' : '') + (err ? ' aria-invalid="true"' : '');
};
const text = (path, label, value, o) => {
  o = o || {};
  const id = uid('f');
  return field(id, label, '<input type="text" id="' + id + '"' + attr(path) + ' value="' + e(value) + '" autocomplete="off"' +
    (o.placeholder ? ' placeholder="' + e(o.placeholder) + '"' : '') + (o.enter ? ' data-pp-enter="' + e(o.enter) + '"' : '') + described(id, o.err, o.hint) + ' />', o.err, o.hint);
};
const area = (path, label, value, hint, err) => {
  const id = uid('f');
  return field(id, label, '<textarea id="' + id + '" rows="4"' + attr(path) + described(id, err, hint) + '>' + e(value) + '</textarea>', err, hint);
};
const radios = (path, legend, value, opts) => {
  const name = uid('r');
  return '<fieldset class="pp-radios"><legend>' + e(legend) + '</legend>' + opts.map((op) => {
    const id = uid('o');
    return '<label for="' + id + '"><input type="radio" id="' + id + '" name="' + name + '"' + attr(path) + ' value="' + e(op[0]) + '"' + (op[0] === value ? ' checked' : '') + ' /> ' + e(op[1]) + '</label>';
  }).join('') + '</fieldset>';
};
const notes = () => {
  const list = s.notes || [];
  if (!list.length) { return ''; }
  const bad = list.some((x) => x.kind === 'bad');
  return '<div class="pp-notes" role="' + (bad ? 'alert' : 'status') + '">' + list.map((x) => '<p class="pp-note ' + e(x.kind) + '">' + e(x.text) + '</p>').join('') + '</div>';
};
const errorSummary = () => {
  const keys = Object.keys(s.errors || {});
  if (!keys.length) { return ''; }
  return '<div class="pp-note bad" role="alert" data-pp-focus><strong>Fix ' + keys.length + ' field' + (keys.length > 1 ? 's' : '') + ':</strong><ul>' + keys.map((k) => '<li>' + e(s.errors[k]) + '</li>').join('') + '</ul></div>';
};
const auto = (value, ms, label) => '<div id="pp-auto" data-value="' + e(value) + '" data-delay="' + Math.round(ms) + '" hidden></div><p class="pp-hint" aria-live="off">' + e(label) + '</p>';
const head = (title, lead) => '<h1 tabindex="-1"' + (Object.keys(s.errors || {}).length ? '' : ' data-pp-focus') + '>' + e(title) + '</h1>' + (lead ? '<p class="pp-lead">' + lead + '</p>' : '');
const STATE_SYM = { Live: '✓', 'Needs approval': '⏸', Failed: '✕', Running: '●', Waiting: '○', Canceled: '–' };
const strip = (envs, source) => '<ol class="pp-strip" aria-label="Rollout">' +
  (source ? '<li class="pp-chip src"><span class="pp-env">' + e(source) + '</span><span class="pp-st">Source env (pipeline plan job only)</span></li>' : '') +
  cfg.rollout.map((x) => {
    const v = (envs && envs[x]) || { state: 'Waiting', detail: '' };
    const cls = v.state.toLowerCase().replace(/[^a-z]+/g, '-');
    return '<li class="pp-chip ' + cls + '"><span class="pp-env">' + e(x) + '</span><span class="pp-st"><span aria-hidden="true">' + (STATE_SYM[v.state] || '') + ' </span>' + e(v.state) + '</span>' +
      (v.detail ? '<span class="pp-detail">' + e(v.detail) + '</span>' : '') + '</li>';
  }).join('') + '</ol>';
const summary = () => s.req ? '<dl class="pp-sum"><dt>Source env</dt><dd>' + e(s.req.source) + '</dd><dt>Flow</dt><dd>' + e(s.req.flow.name) +
  '</dd><dt>Request</dt><dd><code>' + e(s.req.requestId) + '</code></dd><dt>Rollout</dt><dd>' + e(cfg.rollout.join(' → ')) + '</dd></dl>' : '';
// Field errors: the app's own checks first, then a field the job refused (error.field).
const errFor = (path) => (s.errors || {})[path] || (s.jobField && s.jobField.field === path ? 'GitLab: ' + s.jobField.message : '');
const compactNotice = (r, jobUrl) => (r.compact ? '<div class="pp-note warn" role="status"><strong>Summary only.</strong> GitLab did not hand over result.json, so the app read the closing summary of the job log. ' +
  'The full change list, the diff and the list of required secrets are not available here' + (jobUrl ? '. ' + link(jobUrl, 'Open the job') + ' for the details' : '') + '.</div>' : '');
const changesTable = (r) => {
  const unknown = r.changes.filter((c) => c.action === 'unknown').length;
  const line = contract.ACTION_ORDER.filter((a) => (a === 'unknown' ? unknown : r.summary[a]))
    .map((a) => (a === 'unknown' ? unknown + ' unrecognised' : r.summary[a] + ' ' + (contract.ACTIONS[a].count || contract.ACTIONS[a].label.toLowerCase()))).join(', ');
  if (r.compact) { return '<p>' + (line ? e(line) + '.' : 'The job reported no counts.') + '</p>'; }
  if (!r.changes.length) { return '<p class="pp-empty">The job reported no changes.</p>'; }
  const row = (c) => {
    const a = contract.ACTIONS[c.action] || { sym: '?', label: 'Not recognised', cls: 'warn' };
    return '<tr><td><code>' + e(c.address) + '</code>' + (c.role === 'selected' ? ' <span class="pp-tag">selected flow</span>' : '') +
      (c.from ? '<br><span class="pp-hint">was <code>' + e(c.from) + '</code></span>' : '') + (c.file ? '<br><span class="pp-hint">' + e(c.file) + '</span>' : '') + '</td>' +
      '<td class="' + a.cls + '"><span aria-hidden="true">' + e(a.sym) + ' </span>' + e(a.label) + (c.reason ? '<br><span class="pp-hint">' + e(c.reason) + '</span>' : '') + '</td>' +
      '<td class="' + (c.status === 'changed-in-both' || c.status === 'git-newer' ? 'warn' : 'same') + '">' + e(contract.ROW_STATUS[c.status] || '') + '</td></tr>';
  };
  const main = r.changes.filter((c) => c.action !== 'unchanged');
  const same = r.changes.filter((c) => c.action === 'unchanged');
  const table = (rows) => '<table class="pp-table"><thead><tr><th scope="col">Shared definition (address)</th><th scope="col">Predicted change</th><th scope="col">Source vs Git</th></tr></thead><tbody>' + rows.map(row).join('') + '</tbody></table>';
  return '<p>' + e(line) + '.</p>' + (main.length ? table(main) : '') + (same.length ? '<details><summary>' + same.length + ' matching Git</summary>' + table(same) + '</details>' : '');
};
const shown = (v) => (v && typeof v === 'object' ? JSON.stringify(v) : (v === undefined || v === null ? '' : String(v)));
// Same in all environments (inlineValues): one tick per variable. Every box for the same variable is kept in step by
// the screen's script, so the last one in the form always holds the person's choice.
const inlineBox = (v, editable) => {
  const id = uid('c');
  const on = (s.req.options.inline || []).indexOf(v) >= 0;
  return (editable ? '<label class="pp-check" for="' + id + '"><input type="checkbox" id="' + id + '"' + attr(['inline', v]) + (on ? ' checked' : '') +
    ' aria-describedby="' + id + '-d" /> Same in all environments: use the source value for <code>' + e(v) + '</code></label>'
    : '<p>Same in all environments: <code>' + e(v) + '</code></p>') +
    '<p class="pp-hint" id="' + id + '-d"><span class="pp-tag">identical everywhere</span> ' + (on ? 'The source value is written as a literal, so ' : 'If ticked, the source value is written as a literal, so ') +
    e(cfg.rollout.join(', ')) + ' all get the same value. It is not asked per environment.</p>';
};
const readiness = (r, editable) => {
  const bl = (r && r.blockers) || {};
  const inl = s.req.options.inline || [];
  // The rollout envs always; the source and the whole request only when there is something for them.
  const envs = cfg.rollout.slice();
  if (bl[s.req.source] || (s.req.options.env || {})[s.req.source]) { envs.unshift(s.req.source); }
  if (bl[contract.REQUEST_BLOCKERS]) { envs.unshift(contract.REQUEST_BLOCKERS); }
  const shownInline = [];
  const rows = envs.map((env) => {
    const list = bl[env] || [];
    const vals = env === contract.REQUEST_BLOCKERS ? {} : ((s.req.options.env || {})[env] || {});
    const items = list.map((b) => {
      if (b.code === 'missing_value' && b.var && env !== contract.REQUEST_BLOCKERS) {
        shownInline.push(b.var);
        if (inl.indexOf(b.var) >= 0) { return '<div class="pp-note ok"><p><code>' + e(b.var) + '</code> has the same value in every environment.</p>' + inlineBox(b.var, editable) + '</div>'; }
        if (editable) {
          return text(['env', env, b.var], env + ': ' + b.var, shown(vals[b.var]), { err: errFor('envValues.' + env + '.' + b.var), hint: (b.message || 'Environment value for ' + env) + '. Text, or JSON for a list.' }) + inlineBox(b.var, true);
        }
      }
      const what = contract.BLOCKER_TEXT[b.code] || b.code || 'blocked';
      return '<p class="pp-note bad"><strong>' + e(what) + '.</strong> ' + e(b.message) + (b.address ? ' <code>' + e(b.address) + '</code>' : '') + (b.var ? ' <code>' + e(b.var) + '</code>' : '') + '</p>';
    }).join('');
    const extra = Object.keys(vals).filter((k) => !list.some((b) => b.var === k)).map((k) => text(['env', env, k], env + ': ' + k, shown(vals[k]), { err: errFor('envValues.' + env + '.' + k) }) + inlineBox(k, true)).join('');
    const ready = !list.length;
    const name = env === contract.REQUEST_BLOCKERS ? 'Whole request' : env + (env === s.req.source ? ' (source env)' : '');
    return '<section class="pp-env-row"><h3>' + e(name) + ' <span class="' + (ready ? 'add' : 'bad') + '">' + (ready ? (r ? '✓ Ready' : 'Not checked yet') : '✕ ' + list.length + ' to fix') + '</span></h3>' + items + (editable ? extra : '') + '</section>';
  }).join('');
  const rest = inl.filter((v) => shownInline.indexOf(v) < 0);
  const same = rest.length ? '<section class="pp-env-row"><h3>Same in all environments <span class="add">identical everywhere</span></h3>' + rest.map((v) => inlineBox(v, editable)).join('') + '</section>' : '';
  return '<h2>Readiness</h2><p class="pp-hint">Environment values each rollout env needs. Missing values block the request; fill them in here and preview again.</p>' + rows + same;
};
const secretsBlock = (r, editable) => {
  const list = (r && r.requiredSecrets) || [];
  if (!list.length) { return ''; }
  const o = s.req.options;
  return '<h2>Secrets</h2><p class="pp-hint">The app never sees secret values. Each secret below is read from Vault in every environment. Vault is not checked from here.</p>' +
    list.map((x) => {
      const id = uid('c');
      const where = '<p class="pp-hint">Terraform variable <code>' + e(x.varName || x.key) + '</code>, Vault field <code>' + e(x.field) + '</code>' + (x.vault ? ' at <code>' + e(x.vault) + '</code>' : '') + '</p>';
      const state = x.declared ? '<p>Already declared in customer_secrets.yml.</p>' : '<p class="warn">New: this request adds it to customer_secrets.yml, so its value must exist in Vault for every environment.</p>';
      const tick = x.declared ? '' : (editable
        ? '<label class="pp-check" for="' + id + '"><input type="checkbox" id="' + id + '"' + attr(['secrets', x.key, 'ok']) + ((o.confirmed || {})[x.key] ? ' checked' : '') + ' /> Its value is in Vault for ' + e(cfg.rollout.join(', ')) + '</label>'
        : '<p>' + ((o.confirmed || {})[x.key] ? '✓ Confirmed in Vault' : 'Not confirmed yet') + '</p>');
      const map = editable ? text(['secrets', x.key, 'mapTo'], 'Use an existing customer_secrets.yml name instead (optional)', (o.secrets || {})[x.key] || '', { err: errFor('secretMappings.' + x.key) })
        : ((o.secrets || {})[x.key] ? '<p>Mapped to <code>' + e(o.secrets[x.key]) + '</code></p>' : '');
      return '<fieldset class="pp-secret"><legend><code>' + e(x.key) + '</code></legend>' + where + state + map + tick + '</fieldset>';
    }).join('');
};
const errorBody = (err) => {
  const t = contract.ERROR_TEXT[err.code] || contract.ERROR_TEXT.unknown;
  const cands = err.candidates && err.candidates.length ? '<p>Several flows match. Search again and choose one of these:</p><ul class="pp-list">' +
    err.candidates.map((c) => '<li>' + e(c.name) + (c.address ? ' <code>' + e(c.address) + '</code>' : '') + '</li>').join('') + '</ul>' : '';
  return '<strong>' + e(t.title) + '.</strong> ' + e(t.fix) + (err.type ? ' <span class="pp-hint">(in ' + e(contract.TYPE_TEXT[err.type] || err.type) + ')</span>' : '') +
    '<p>' + e(err.message) + (err.field ? ' <span class="pp-hint">Field: <code>' + e(err.field) + '</code></span>' : '') + '</p>' + cands +
    (err.retryable ? '<p class="pp-hint">GitLab says this may work if you try again.</p>' : '');
};
const subBanner = () => {
  const sub = s.sub || {};
  if (sub.state === 'unknown') {
    return '<div class="pp-note warn" role="alert"><strong>Outcome unknown.</strong> GitLab did not confirm what happened to your last request. Preview and Submit stay off until Check GitLab finds out.' +
      (sub.message ? ' ' + e(sub.message) : '') + ' ' + link(sub.jobUrl, 'Open the job') + '<div class="pp-actions">' + button('CHECK', 'Check GitLab') + '</div></div>';
  }
  if (sub.state === 'error' && sub.error) {
    const partial = sub.error.code === 'partial' && sub.result ? link(lib.webUrl(cfg, sub.result.branchUrl), 'Open the request branch') : '';
    const err = contract.ERROR_TEXT[sub.error.code] ? sub.error : Object.assign({}, sub.error, { code: 'unknown' });
    return '<div class="pp-note bad" role="alert">Submit failed: ' + errorBody(err) + partial + ' ' + link(sub.jobUrl, 'Open the job') +
      '<p class="pp-hint">' + (sub.error.retryable ? 'You can submit again as it is, or change an option.' : 'Change any option to submit again.') + '</p></div>';
  }
  return '';
};
const actions = () => {
  const pw = lib.canPreview(s);
  const sw = lib.canSubmit(s);
  const cancel = s.req && s.req.lastCommit && s.sub.state !== 'done' && s.sub.state !== 'unknown' ? button('CANCEL', 'Cancel request', { ghost: true, danger: true }) : '';
  // Reasons an edit can fix turn the button back on when the person edits (the server checks again).
  const editable = s.screen === 'options';
  const fieldsOnly = editable && pw === lib.FIX_FIELDS;
  return '<div class="pp-actions">' + button('PREVIEW', 'Preview', { why: pw, ghost: true, onEdit: fieldsOnly }) +
    button('SUBMIT', s.req && s.req.options.mode === 'branch' ? 'Submit request (branch only)' : 'Submit request', { why: sw, onEdit: editable && !!sw && (!pw || fieldsOnly) }) + cancel + '</div>';
};

const screens = {};
screens.search = () => {
  const sr = s.search || {};
  const sources = Object.keys(cfg.sources);
  const id = uid('f');
  const select = '<select id="' + id + '"' + attr(['source']) + '>' + sources.map((x) => '<option value="' + e(x) + '"' + (x === sr.source ? ' selected' : '') + '>' + e(x) + '</option>').join('') + '</select>';
  let results = '';
  if (sr.matches) {
    if (!sr.matches.length) {
      results = '<p class="pp-empty" role="status">No flow in ' + e(sr.source) + ' matches “' + e(sr.query) + '”' + (sr.listed === 0 ? ' (the env has no flows the app can read; ask your DaVinci administrator to check the worker\'s role)' : '') + '. Try part of the name, or the flow ID.</p>';
    } else {
      results = '<h2 role="status">' + (sr.matches.length === 1 ? '1 match' : sr.total + ' matches' + (sr.total > sr.matches.length ? ' (first ' + sr.matches.length + ' shown)' : '') + '. Choose one; the app never picks for you') + '</h2>' +
        '<table class="pp-table"><thead><tr><th scope="col">Main flow</th><th scope="col">Flow ID</th><th scope="col"><span class="pp-sr">Action</span></th></tr></thead><tbody>' +
        sr.matches.map((f, i) => '<tr><td>' + e(f.name) + '</td><td><code>' + e(f.id) + '</code></td><td>' + button('PICK:' + i, 'Use this flow', { ghost: sr.matches.length > 1 }) + '</td></tr>').join('') + '</tbody></table>';
    }
  }
  return head('New request', 'Pick the <strong>source env</strong> and the main flow to bring into Git. Its shared definitions and subflows come with it; the rollout then carries the same commit through ' + e(cfg.rollout.join(' → ')) + '.') +
    errorSummary() + field(id, 'Source env', select, '', '') + text(['query'], 'Main flow name or ID', sr.query || '', { err: (s.errors || {}).query, placeholder: 'for example: Login' }) +
    '<div class="pp-actions">' + button('SEARCH', 'Search flows') + '</div>' + results;
};
screens.options = () => {
  const o = s.req.options;
  const p = s.preview;
  const r = p && p.result;
  const cur = lib.currentPreview(s);
  let state = '<p class="pp-note">No preview yet. Preview runs the request job without writing anything and shows the predicted changes and readiness.</p>';
  if (p && !cur) { state = '<p class="pp-note warn">The options changed since the last preview. Preview again to see their effect.</p>'; }
  if (cur && cur.result) { state = '<p class="pp-note ok">The preview below matches these options.</p>'; }
  if (p && p.failed) { state = '<p class="pp-note bad">The last preview did not finish' + (p.error ? ': ' + e(p.error.message) : (p.message ? ': ' + e(p.message) : '.')) + ' ' + link(p.jobUrl, 'Open the job') + '</p>'; }
  const deps = r ? r.changes.filter((c) => lib.choosable(r).indexOf(c.address) >= 0) : [];
  const depBlock = '<h2>Shared definitions</h2>' + (!r ? '<p class="pp-hint">Preview to list the dependencies that differ from Git. A dependency that differs keeps the Git version unless you choose otherwise, because other flows use it too.</p>'
    : (!deps.length ? '<p class="pp-empty">No dependency differs from Git.</p>' : deps.map((c) => radios(['deps', c.address], c.address + (c.status === 'changed-in-both' ? ' (changed in both Git and the source)' : ''), o.deps[c.address] || (c.action === 'kept_differs' ? 'keep' : 'update'),
      [['keep', 'Keep Git version'], ['update', 'Use source']])).join('')));
  const offered = contract.removeChoices(r, o);
  const confirmId = uid('c');
  const stopBlock = '<h2>Stop managing</h2>' + (!r ? '<p class="pp-hint">Preview to list the shared definitions Git already manages. You can then stop managing one without destroying it in PingOne.</p>'
    : (!offered.length ? '<p class="pp-empty">No shared definition in this export can be stopped. The selected flow never can.</p>'
      : '<p class="pp-hint">These are managed in Git. Stopping removes the definition from Git; the live resource stays in PingOne and nothing is destroyed. The selected flow is never offered.</p>' +
        offered.map((a) => { const id = uid('c'); return '<label class="pp-check" for="' + id + '"><input type="checkbox" id="' + id + '"' + attr(['remove', a]) + (o.remove.indexOf(a) >= 0 ? ' checked' : '') +
          ' /> <code>' + e(a) + '</code> ' + e(contract.STOP_MANAGING) + '</label>'; }).join('') +
        '<div class="pp-field' + (errFor('remove') ? ' has-err' : '') + '">' + (errFor('remove') ? '<p class="pp-err" id="' + confirmId + '-err">' + e(errFor('remove')) + '</p>' : '') +
        '<label class="pp-check" for="' + confirmId + '"><input type="checkbox" id="' + confirmId + '"' + attr(['removeConfirm']) + (o.removeConfirmed ? ' checked' : '') + (errFor('remove') ? ' aria-invalid="true" aria-describedby="' + confirmId + '-err"' : '') +
        ' /> I confirm: Git stops managing every resource ticked above. They keep running in PingOne.</label></div>'));
  const extrasBlock = cfg.pathApplications ? '<h2>Applications and flow policies</h2>' +
    (o.applications.length || o.flowPolicies.length ? '<ul class="pp-list">' + o.applications.map((x) => '<li>Application: ' + e(x.name) + '</li>').join('') +
      o.flowPolicies.map((x) => '<li>Flow policy: ' + e(x.name) + (x.app ? ' <span class="pp-hint">(application ' + e(x.app) + ')</span>' : '') + '</li>').join('') + '</ul>' : '<p class="pp-hint">None. Optional.</p>') +
    '<div class="pp-actions">' + button('EXTRAS', 'Add an application or flow policy', { ghost: true }) + '</div>' : '';
  return head('Review the change', 'Choose how the request is written, then preview it.') + summary() + subBanner() + errorSummary() + state +
    radios(['mode'], 'How to hand the change to GitLab', o.mode, [['mr', 'Open a merge request'], ['branch', 'Branch only (no merge request)']]) +
    '<p class="pp-hint">Request branch: <code>' + e(s.req.branch) + '</code></p>' +
    text(['title'], 'Title', o.title, { err: errFor('title'), hint: 'One line, at most 255 characters.' }) +
    area(['commitMessage'], 'Commit message', o.commitMessage, 'At most 2000 characters. The job adds the Requested-by and Request-id lines.', errFor('commitMessage')) +
    area(['description'], 'Description (optional)', o.description, 'Shown in the merge request. Your name is added for you.', errFor('description')) +
    extrasBlock + depBlock + stopBlock + readiness(r, true) + secretsBlock(r, true) + actions() + '<div class="pp-actions">' + button('BACK', 'Back to flow search', { ghost: true }) + '</div>';
};
// Applications and flow policies, found by name in the source env like flows are: no guessing, every match listed.
screens.extras = () => {
  const o = s.req.options;
  const ex = s.extras || {};
  const found = (res, what, addPrefix, extraBtn) => {
    if (!res || !res.matches) { return ''; }
    if (!res.matches.length) { return '<p class="pp-empty" role="status">No ' + what + ' in ' + e(s.req.source) + ' matches “' + e(res.query) + '”' + (res.listed === 0 ? ' (the env has none the app can read; ask your DaVinci administrator to check the worker\'s role)' : '') + '. Try part of the name.</p>'; }
    return '<h3 role="status">' + (res.matches.length === 1 ? '1 match' : res.total + ' matches' + (res.total > res.matches.length ? ' (first ' + res.matches.length + ' shown)' : '') + '. Choose one; the app never picks for you') + '</h3>' +
      '<table class="pp-table"><thead><tr><th scope="col">Name</th><th scope="col">ID</th><th scope="col"><span class="pp-sr">Action</span></th></tr></thead><tbody>' +
      res.matches.map((m, i) => '<tr><td>' + e(m.name) + '</td><td><code>' + e(m.id) + '</code></td><td>' +
        button(addPrefix + i, 'Add this ' + what, { ghost: res.matches.length > 1 }) + (extraBtn ? extraBtn(i) : '') + '</td></tr>').join('') + '</tbody></table>';
  };
  const added = (list, drop, what) => (list.length ? '<table class="pp-table"><thead><tr><th scope="col">Name</th><th scope="col"><span class="pp-sr">Action</span></th></tr></thead><tbody>' +
    list.map((x, i) => '<tr><td>' + e(x.name) + (x.app ? ' <span class="pp-hint">application ' + e(x.app) + '</span>' : '') + '</td><td>' + button(drop + i, 'Remove ' + what + ' from the request', { ghost: true }) + '</td></tr>').join('') + '</tbody></table>'
    : '<p class="pp-empty">None added.</p>');
  const policies = ex.pol ? '<h2>Flow policies of ' + e(ex.pol.app.name) + '</h2><p class="pp-hint">PingOne lists flow policies per application, so the app reads only this application\'s. ' +
    'The request job finds a policy by name in the whole source env and lists the candidates if another application has one with the same name.</p>' +
    text(['polQuery'], 'Flow policy name (leave empty to list them all)', ex.pol.query || '', { enter: 'SEARCH_POL' }) +
    '<div class="pp-actions">' + button('SEARCH_POL', 'Search flow policies') + '</div>' + found(ex.pol, 'flow policy', 'ADDPOL:') : '';
  return head('Add an application or flow policy', 'Optional. They are found by name in <strong>' + e(s.req.source) + '</strong>, like flows, and come with the request next to ' + e(s.req.flow.name) + '. At most ' + contract.LIMITS.refs + ' of each.') +
    summary() + errorSummary() +
    '<h2>In this request</h2><h3>Applications (' + o.applications.length + ' of ' + contract.LIMITS.refs + ')</h3>' + added(o.applications, 'DROPAPP:', 'application') +
    (cfg.pathFlowPolicies ? '<h3>Flow policies (' + o.flowPolicies.length + ' of ' + contract.LIMITS.refs + ')</h3>' + added(o.flowPolicies, 'DROPPOL:', 'flow policy') : '') +
    '<h2>Find an application</h2>' + text(['appQuery'], 'Application name or ID', (ex.apps && ex.apps.query) || '', { err: (s.errors || {}).appQuery, enter: 'SEARCH_APP' }) +
    '<div class="pp-actions">' + button('SEARCH_APP', 'Search applications') + '</div>' +
    found(ex.apps, 'application', 'ADDAPP:', cfg.pathFlowPolicies ? (i) => button('POLICIES:' + i, 'Its flow policies', { ghost: true }) : null) + policies +
    '<div class="pp-actions">' + button('EXTRAS_DONE', 'Back to the options') + '</div>';
};
screens.working = () => {
  const run = s.run;
  const age = now - run.startedAt;
  const what = run.kind === 'preview' ? 'Preview' : 'Submit';
  let step = 'GitLab has the request. Waiting for the pipeline to start';
  if (run.pipelineId && !run.jobId) { step = 'Pipeline started. Waiting for the ' + contract.REQUEST_JOB + ' job'; }
  if (run.jobId) { step = run.waitingTurn ? 'The job is waiting for another request to finish (one at a time)' : (run.waitingRunner ? 'The job is waiting for a runner' : 'The job is running'); }
  const slow = [];
  if (run.noPipeline) { slow.push('GitLab has not started a pipeline for the request after ' + lib.dur(age) + '. GitLab may be busy, or CI may be off for request branches.'); }
  if (run.waitingRunner && age > cfg.pipelineAppearSeconds * 1000) { slow.push('No runner has picked up the job yet. GitLab runners may be busy.'); }
  const delay = run.polls < 1 ? 3000 : (run.polls < 2 ? 5000 : 10000);
  const links = [link(run.pipelineUrl, 'Open the pipeline'), link(run.jobUrl, 'Open the job')].filter((x) => x).join(' · ');
  let body = '<div class="pp-wait" role="status" aria-live="polite"><div class="pp-spin" aria-hidden="true"></div><p><strong>' + e(step) + '</strong> (' + e(lib.dur(age)) + ')</p></div>';
  if (slow.length) { body += '<div class="pp-note warn" role="status">' + slow.map((x) => '<p>' + e(x) + '</p>').join('') + '</div>'; }
  if (run.timedOut) {
    body += '<div class="pp-note warn" role="alert"><strong>GitLab is slow.</strong> The app stopped checking after ' + e(lib.dur(age)) + '. ' +
      (run.kind === 'submit' ? 'The outcome of your submit is unknown until GitLab answers.' : 'The preview may still finish.') + '</div>' +
      '<div class="pp-actions">' + button('KEEP', 'Keep waiting') + button('CHECK', 'Check GitLab', { ghost: true }) + '</div>';
  } else {
    body += auto('POLL', delay, 'Checks again by itself in ' + Math.round(delay / 1000) + ' seconds.') + '<div class="pp-actions">' + button('POLL', 'Check now', { ghost: true }) + '</div>';
  }
  return head(what + ' running', run.kind === 'preview' ? 'The request job runs a preview. Nothing is written.' : 'The request job writes the change to the request branch' + (s.req.options.mode === 'mr' ? ' and opens a merge request.' : '.')) +
    summary() + body + (links ? '<p>' + links + '</p>' : '') + '<div class="pp-actions">' + button('STOP', 'Stop waiting', { ghost: true }) + '</div>';
};
screens.preview = () => {
  const p = s.preview || {};
  const r = p.result;
  let body = '';
  if (p.failed) {
    body = '<div class="pp-note bad" role="alert"><strong>The preview did not finish.</strong> ' + e(p.message || (p.error && p.error.message) || '') + ' ' + link(p.jobUrl, 'Open the job') +
      (p.noResult ? '<div class="pp-actions">' + button('CHECK', 'Check GitLab') + '</div>' : '') + '</div>';
  } else if (r) {
    if (r.error) { body += '<div class="pp-note bad" role="alert">' + errorBody(r.error) + '</div>'; }
    body += compactNotice(r, p.jobUrl);
    if (r.selected.length) { body += '<p>Selected: ' + r.selected.map((x) => e(x.name) + ' <code>' + e(x.address) + '</code>').join(', ') + '</p>'; }
    if (r.warnings.length) { body += '<div class="pp-note warn">' + r.warnings.map((x) => '<p>' + e(x) + '</p>').join('') + '</div>'; }
    if (r.problems.length) { body += '<div class="pp-note warn">' + r.problems.map((x) => '<p>' + e(x) + '</p>').join('') + '</div>'; }
    if (lib.onlyUnchanged(r)) { body += '<p class="pp-empty">No changes: Git already matches ' + e(s.req.source) + ' for this flow.</p>'; }
    body += '<h2>Predicted changes</h2>' + changesTable(r) + (r.diffUrl && lib.webUrl(cfg, r.diffUrl) ? '<p>' + link(lib.webUrl(cfg, r.diffUrl), 'Review the change in GitLab') + '</p>' : '') +
      readiness(r, false) + secretsBlock(r, false);
  }
  const stale = p.key && s.req && p.key !== lib.optionsKey(s.req.options) ? '<p class="pp-note warn">The options changed after this preview.</p>' : '';
  return head(r && !r.error && !lib.blockerCount(r) ? 'Preview: ready' : 'Preview', 'Result of the request job, without writing anything. ' + link(p.jobUrl, 'Open the job')) +
    summary() + subBanner() + stale + body + actions() + '<div class="pp-actions">' + button('EDIT', 'Edit options', { ghost: true }) + '</div>';
};
screens.submitted = () => {
  const r = (s.sub && s.sub.result) || {};
  const mr = lib.webUrl(cfg, r.mrUrl);
  const br = lib.webUrl(cfg, r.branchUrl);
  if (r.status === 'no_changes') {
    return head('No changes', 'Git already matches ' + e(s.req.source) + ' for this flow. The job wrote nothing and opened no merge request.') + summary() +
      '<div class="pp-actions">' + button('NAV_NEW', 'New request') + '</div>';
  }
  const again = r.status === 'already_submitted' ? '<p class="pp-note">This request was already submitted. Nothing new was written; this is the existing ' + (mr ? 'merge request' : 'branch') + '.</p>' : '';
  return head('Request submitted', mr ? 'The merge request is open. The rollout starts after it is merged.' : 'The change is on the request branch. Nothing rolls out until it is merged.') +
    summary() + again + (s.sub.found ? '<p class="pp-note">Found in GitLab after Check GitLab.</p>' : '') +
    '<p class="pp-big">' + (mr ? link(mr, 'Open merge request') : link(br, 'Open the branch')) + '</p>' +
    (r.diffUrl ? '<p>' + link(r.diffUrl, 'Review the commit in GitLab') + '</p>' : '') + compactNotice(r, s.sub.jobUrl) + (r.compact ? '<h2>Summary</h2>' + changesTable(r) : '') +
    (r.changes && r.changes.length ? '<h2>Changes written</h2>' + changesTable(r) : '') +
    '<div class="pp-actions">' + button('WATCH', 'Watch the rollout') + button('NAV_NEW', 'New request', { ghost: true }) + '</div>';
};
screens.status = () => {
  const st = s.st;
  let lead = '';
  if (!st.mr) { lead = st.mode === 'branch' ? 'Branch only. Nothing rolls out until someone opens and merges a merge request from ' + e(st.branch) + '.' : 'Looking for the merge request.'; }
  else if (st.mr.state === 'opened') { lead = 'Waiting for the merge request to be merged. ' + link(st.mr.webUrl, 'Open merge request'); }
  else if (st.mr.state === 'closed') { lead = 'The merge request was closed without merging. Nothing rolls out.'; }
  else if (st.mr.state === 'merged') { lead = 'Merged. The pipeline on ' + e(cfg.defaultBranch) + ' carries the same commit through each environment. ' + link(st.mr.webUrl, 'Open merge request') + (st.pipeline ? ' · ' + link(st.pipeline.webUrl, 'Open the pipeline') : ''); }
  const checked = st.checkedAt ? 'Last checked ' + new Date(st.checkedAt).toISOString().slice(11, 19) + ' UTC.' : '';
  let refresh = '';
  if (st.final) { refresh = '<p class="pp-hint">' + e(checked) + ' The rollout has finished; no more automatic checks.</p>'; }
  else if (st.paused) { refresh = '<p class="pp-hint">' + e(checked) + ' Automatic checks are paused.</p><div class="pp-actions">' + button('RESUME', 'Resume automatic checks', { ghost: true }) + '</div>'; }
  else if (st.expired) { refresh = '<p class="pp-note warn">' + e(checked) + ' Automatic checks stopped after ' + cfg.statusMaxMinutes + ' minutes.</p><div class="pp-actions">' + button('RESUME', 'Resume automatic checks', { ghost: true }) + '</div>'; }
  else if (st.auto) { refresh = auto('REFRESH', cfg.statusRefreshSeconds * 1000, checked + ' Checks again every ' + cfg.statusRefreshSeconds + ' seconds.') + '<div class="pp-actions">' + button('PAUSE', 'Pause automatic checks', { ghost: true }) + '</div>'; }
  return head('Rollout: ' + (st.title || st.branch || ''), lead) + strip(st.envs, s.req ? s.req.source : '') + refresh +
    '<div class="pp-actions">' + button('REFRESH', 'Check now', { ghost: true }) + button('NAV_REQUESTS', 'All requests', { ghost: true }) + '</div>';
};
screens.requests = () => {
  const rq = s.requests || { list: [], branches: [] };
  const rows = rq.list.map((m, i) => '<tr><td>' + link(m.webUrl, '!' + m.iid) + '</td><td>' + e(m.title) + '</td><td>' + e(m.requestedBy || 'not recorded') + '</td><td>' + e(m.state) + '</td><td>' + e(m.updatedAt) +
    '</td><td>' + button('OPEN:' + i, 'Rollout', { ghost: true }) + '</td></tr>').join('');
  const KIND = { preview: 'Previewed, not submitted', submit: 'Submitted; job running or failed', written: 'Branch only' };
  const brows = rq.branches.map((b, i) => '<tr><td>' + link(b.webUrl, b.name) + '</td><td>' + e(KIND[b.kind]) + '</td><td>' + e(b.updatedAt) + '</td><td>' +
    (b.kind === 'written' ? button('OPENB:' + i, 'Rollout', { ghost: true }) : '') + (b.cancellable ? button('CANCELB:' + i, 'Cancel request', { ghost: true, danger: true }) : '') + '</td></tr>').join('');
  return head('Requests', 'Requests the app has made, newest first.') +
    (rows ? '<h2>Merge requests</h2><table class="pp-table"><thead><tr><th scope="col">MR</th><th scope="col">Title</th><th scope="col">Requested by</th><th scope="col">State</th><th scope="col">Updated (UTC)</th><th scope="col"><span class="pp-sr">Action</span></th></tr></thead><tbody>' + rows + '</tbody></table>'
      : '<p class="pp-empty">No merge requests with the ' + e(cfg.mrLabel) + ' label yet.</p>') +
    (brows ? '<h2>Request branches without a merge request</h2><table class="pp-table"><thead><tr><th scope="col">Branch</th><th scope="col">State</th><th scope="col">Updated (UTC)</th><th scope="col"><span class="pp-sr">Action</span></th></tr></thead><tbody>' + brows + '</tbody></table>' : '') +
    '<div class="pp-actions">' + button('REFRESH', 'Refresh', { ghost: true }) + '</div>';
};

const screen = screens[s.screen] && (s.screen !== 'options' && s.screen !== 'preview' && s.screen !== 'working' && s.screen !== 'extras' || s.req) && (s.screen !== 'working' || s.run) && (s.screen !== 'status' || s.st) ? s.screen : 'search';
s.screen = screen;
const nav = '<nav class="pp-nav" aria-label="App">' + button('NAV_NEW', 'New request', { ghost: screen !== 'search' }) + button('NAV_REQUESTS', 'Requests', { ghost: screen !== 'requests' }) +
  '<span class="pp-user">Signed in as ' + e(s.user.name || s.user.username) + '</span>' + button('SIGNOUT', 'Leave the app', { ghost: true }) + '</nav>';
const html = nav + '<main class="pp-main" id="pp-content">' + notes() + screens[screen]() + '</main>';
s.notes = [];
return { state: s, html: html };
