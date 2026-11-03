// Stage 6: timing table, verdict and the "copy results" JSON. The results hold
// counts, sizes and timings only: no names, IDs, URLs, tokens or values.
const s = lib.state(params.state);
const t = lib.now();
const E = lib.escapeHtml;
const total = s.startTs ? t - s.startTs : 0;
if (s.segStart) { s.totals.maxSegmentMs = Math.max(s.totals.maxSegmentMs, t - s.segStart); }
const set = s.settings || { flowTimeoutSeconds: 300, httpTimeoutSeconds: 15 };
const tot = s.totals;
const fetched = (s.fetch && s.fetch.ok) || 0;
const wanted = s.fetch ? s.fetch.ids.length : 0;
const tf = s.tfvars || null;
const checks = [];
const rank = { PASS: 0, SKIP: 0, WARN: 1, FAIL: 2 };
const check = (id, result, detail) => checks.push({ id: id, result: result, detail: detail });
const band = (v, pass, warn) => (v <= pass ? 'PASS' : (v <= warn ? 'WARN' : 'FAIL'));
const sec = (ms) => (ms / 1000).toFixed(1) + ' s';
const kb = (b) => (b >= 1048576 ? (b / 1048576).toFixed(2) + ' MB' : (b / 1024).toFixed(1) + ' KB');

// Per-stage rows.
const rows = lib.STAGES.map((name) => {
  const st = s.stages[name];
  if (!st) { return { stage: name, status: 'not reached' }; }
  const wall = st.interleaved ? st.fnMs : (st.startTs ? (st.endTs || t) - st.startTs : 0);
  return { stage: name, status: st.status === 'running' ? 'stopped' : st.status, ms: wall, calls: st.calls, callMs: st.callMs, maxCallMs: st.maxCallMs,
    fnMs: st.fnMs, maxFnMs: st.maxFnMs, items: st.items, bytesIn: st.bytesIn, bytesOut: st.bytesOut, errors: st.errors };
});

if (s.invalid || !s.cfg) {
  check('run_started', 'FAIL', 'The run did not start: fix the inputs or settings listed under errors.');
} else {
  for (const r of rows) {
    if (r.status === 'error' || r.status === 'stopped') { check('stage_' + r.stage, 'FAIL', 'Stage ' + r.stage + ' ' + (r.status === 'error' ? 'failed' : 'did not finish') + '.'); }
    if (r.status === 'warn') { check('stage_' + r.stage, 'WARN', 'Stage ' + r.stage + ' finished with errors (see errors).'); }
    if (r.status === 'refused') { check('stage_' + r.stage, 'WARN', 'GitLab write refused by a safety check (see errors); rerun with a new branch name.'); }
    if (r.status === 'not reached' && r.stage !== 'gitlab') { check('stage_' + r.stage, 'FAIL', 'Stage ' + r.stage + ' was not reached.'); }
  }
  const n = Math.max(fetched, wanted, 1);
  const budget = 10000 + 2500 * n;
  check('total_time', band(total, budget, 2 * budget), 'Total ' + sec(total) + ' for ' + n + ' flow(s); budget ' + sec(budget) + ' (10 s + 2.5 s per flow), WARN up to ' + sec(2 * budget) + '.');
  const timeoutMs = set.flowTimeoutSeconds * 1000;
  const worst = rows.filter((r) => r.ms !== undefined).reduce((a, r) => (r.ms > a.ms ? r : a), { stage: '-', ms: 0 });
  check('flow_timeout_margin', band(total / timeoutMs, 0.5, 0.8), 'Run used ' + Math.round(100 * total / timeoutMs) + '% of the ' + set.flowTimeoutSeconds +
    ' s flow timeout (longest stage: ' + worst.stage + ', ' + sec(worst.ms) + '). PASS under 50%, WARN under 80%.');
  check('http_timeout_margin', band(tot.maxCallMs / (set.httpTimeoutSeconds * 1000), 0.5, 0.8), 'Slowest call ' + sec(tot.maxCallMs) + ' against the ' +
    set.httpTimeoutSeconds + ' s HTTP timeout. PASS under 50%, WARN under 80%.');
  check('browser_request', band(tot.maxSegmentMs, 20000, 40000), 'Longest single browser request ' + sec(tot.maxSegmentMs) + ' (' + tot.segments +
    ' requests, ' + tot.handbacks + ' hand-backs). PASS up to 20 s, WARN up to 40 s.');
  check('function_time', band(tot.maxFnMs, 1000, 3000), 'Slowest custom function run ' + tot.maxFnMs + ' ms. PASS up to 1 s, WARN up to 3 s.');
  check('response_size', band(tot.maxResponseBytes, 1048576, 5242880), 'Largest response ' + kb(tot.maxResponseBytes) + '. PASS up to 1 MB, WARN up to 5 MB.');
  check('carried_state', band(tot.maxFragsBytes, 2097152, 8388608), 'Largest flow-instance variable ' + kb(tot.maxFragsBytes) + '. PASS up to 2 MB, WARN up to 8 MB.');
  if (s.fetch) {
    const translated = (s.stages.translate && s.stages.translate.items) || 0;
    check('coverage', fetched === wanted && translated === fetched ? 'PASS' : 'FAIL', fetched + ' of ' + wanted + ' requested flows fetched, ' + translated + ' translated.');
  }
  if (tf) {
    const issues = tf.unresolvedIds + tf.problems + ((s.counts && (s.counts.flowNameCollisions + s.counts.connectionNameCollisions)) || 0);
    check('translation', issues ? 'WARN' : 'PASS', tf.unresolvedIds + ' unresolved raw IDs, ' + tf.problems + ' translation problems, ' +
      ((s.counts && s.counts.flowNameCollisions) || 0) + ' flow name collisions; ' + tf.secretsMasked + ' secrets replaced by placeholders.');
  }
  if (!s.gitlab) { check('gitlab_write', 'SKIP', 'No branch given: GitLab write skipped.'); } else if (s.gl && s.gl.committed) { check('gitlab_write', 'PASS', 'Committed ' + s.gl.path + ' to the new branch' + (s.gl.mrOpened ? ' and opened a merge request.' : '.')); }
}
const verdict = checks.reduce((a, c) => (rank[c.result] > rank[a] ? c.result : a), 'PASS');

const results = {
  test: 'pingone-promote-davinci-feasibility', version: lib.VERSION, runAt: s.runAt || '', level: s.level || '', verdict: verdict,
  settings: s.settings || {}, totalMs: total,
  calibration: s.calib ? { loops: s.calib.i, avgIterationMs: s.calib.i > 1 ? Math.round(s.calib.sumMs / (s.calib.i - 1)) : 0, maxIterationMs: s.calib.maxMs } : null,
  stages: rows, totals: tot, counts: s.counts || {}, fetch: s.fetch ? { requested: wanted, ok: s.fetch.ok, failed: s.fetch.failed, maxFlowBytes: s.fetch.maxFlowBytes || 0 } : null,
  tfvars: tf, secretsNeeded: (s.secretKeys || []).length,
  gitlab: s.gitlab ? { committed: !!(s.gl && s.gl.committed), fileAction: (s.gl && s.gl.fileAction) || '', mrRequested: !!s.openMr, mrOpened: !!(s.gl && s.gl.mrOpened), commitBytes: (s.gl && s.gl.commitBytes) || 0 } : null,
  checks: checks, errors: s.errors, notes: s.notes
};
const json = JSON.stringify(results, null, 2);

const cls = (r) => (r === 'PASS' || r === 'ok' ? 'ok' : r === 'WARN' || r === 'warn' || r === 'refused' ? 'warn' : r === 'SKIP' || r === 'skipped' || r === 'not reached' ? '' : 'bad');
const table = '<table class="fs-table"><thead><tr><th>Stage</th><th>Status</th><th>Time</th><th>Calls</th><th>Call time (max)</th><th>Function time (max)</th>' +
  '<th>Items</th><th>In</th><th>Out</th><th>Errors</th></tr></thead><tbody>' + rows.map((r) => (r.ms === undefined
  ? '<tr><td>' + E(r.stage) + '</td><td>' + E(r.status) + '</td><td colspan="8"></td></tr>'
  : '<tr><td>' + E(r.stage) + '</td><td class="' + cls(r.status) + '">' + E(r.status) + '</td><td>' + E(sec(r.ms)) + '</td><td>' + r.calls + '</td><td>' +
    E(sec(r.callMs)) + ' (' + E(sec(r.maxCallMs)) + ')</td><td>' + r.fnMs + ' ms (' + r.maxFnMs + ' ms)</td><td>' + r.items + '</td><td>' + E(kb(r.bytesIn)) +
    '</td><td>' + E(kb(r.bytesOut)) + '</td><td>' + r.errors + '</td></tr>')).join('') +
  '<tr class="fs-total"><td>Total</td><td class="' + cls(verdict) + '">' + verdict + '</td><td>' + E(sec(total)) + '</td><td>' + tot.httpCalls + '</td><td>' +
  E(sec(tot.httpMs)) + ' (' + E(sec(tot.maxCallMs)) + ')</td><td>' + tot.fnMs + ' ms (' + tot.maxFnMs + ' ms)</td><td></td><td>' + E(kb(tot.bytesIn)) +
  '</td><td>' + E(kb(tot.bytesOut)) + '</td><td>' + s.errors.length + '</td></tr></tbody></table>';
const checkList = '<ul class="fs-checks">' + checks.map((c) => '<li><b class="' + cls(c.result) + '">' + c.result + '</b> <code>' + E(c.id) + '</code> ' + E(c.detail) + '</li>').join('') + '</ul>';
const errList = s.errors.length ? '<h2>Errors</h2><ul class="fs-checks">' + s.errors.map((x) => '<li><code>' + E(x.where) + '</code> HTTP ' + E(x.status) +
  (x.code ? ' <code>' + E(x.code) + '</code>' : '') + (x.message ? ' ' + E(x.message) : '') + '</li>').join('') + '</ul>' : '';
const notes = s.notes.length ? '<h2>Notes</h2><ul class="fs-checks">' + s.notes.map((x) => '<li>' + E(x) + '</li>').join('') + '</ul>' : '';
// customer_secrets.yml entries a real submission would need (key names only).
const keys = s.secretKeys || [];
const secretsBlock = '<h2>Secrets a real submission needs (' + keys.length + ')</h2>' + (keys.length
  ? '<p class="fs-lead">Entries for <code>customer_secrets.yml</code> (layout to confirm with the pipeline team):</p><pre>' +
    E(keys.map((k) => 'TF_VAR_' + k + ': apps/pingone-davinci/${VAULT_PATH_ENV}/pingone/' + k + '@secret').join('\n')) + '</pre>'
  : '<p class="fs-lead">None: the translated flows, connections and variables hold no secrets.</p>');
const counts = s.counts ? '<p class="fs-lead">Environment: ' + s.counts.flowsInEnv + ' flows, ' + s.counts.connectorInstances + ' connector instances, ' +
  s.counts.variables + ' variables. Snippet: ' + (tf ? tf.flows + ' flows (tiers t0..t3: ' + [0, 1, 2, 3].map((i) => tf.tiers['t' + i]).join('/') + '), ' +
  tf.connections + ' connections, ' + tf.variables + ' variables, ' + E(kb(tf.bytes)) + ', ' + tf.lines + ' lines.' : 'not built.') + '</p>' : '';
const cal = results.calibration && results.calibration.loops > 1 ? '<p class="fs-lead">Calibration: ' + results.calibration.avgIterationMs +
  ' ms per no-HTTP loop iteration (2 connector nodes and 3 evaluators), max ' + results.calibration.maxIterationMs + ' ms.</p>' : '';
const mr = s.gl && s.gl.mrUrl ? '<p class="fs-note ok">Merge request: <a href="' + E(s.gl.mrUrl) + '" target="_blank" rel="noopener">' + E(s.gl.mrUrl) + '</a></p>' : '';
const tfText = String(params.tfvars || '');
const preview = tfText ? '<details><summary>tfvars preview (first 8 KB; not formatted by terraform fmt)</summary><pre>' + E(tfText.slice(0, 8192)) +
  (tfText.length > 8192 ? '\n...' : '') + '</pre></details>' : '';
const html = '<div class="fs-verdict ' + cls(verdict) + '">' + verdict + '<small>' + E(sec(total)) + ' total, level ' + E(s.level || '-') + '</small></div>' +
  counts + cal + table + '<h2>Checks</h2>' + checkList + errList + notes + mr + secretsBlock + preview;
return { verdict: verdict, resultHtml: html, resultsText: E(json) };
