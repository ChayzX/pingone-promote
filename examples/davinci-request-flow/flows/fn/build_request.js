// Parse the selection form and build request.json exactly as the Go app's
// preview does (gitops.NewRequest + Encode). Also validates the GitLab
// settings, so every GitLab URL used later comes from a checked value.
const fail = (m) => ({ state: 'error', error: core.escapeHtml(m), requestId: '', requestJson: '', gitlabApi: '', previewText: '', summaryText: '' });
const selected = String(params.selectedRaw || '').split(/[\s,]+/).filter((s) => s.length > 0);
for (const id of selected) {
  if (!core.SAFE_ID.test(id)) { return fail('Resource ID "' + id.slice(0, 80) + '" is not a valid ID.'); }
}
const mappings = {};
for (const line of String(params.mappingsRaw || '').split(/\r?\n/).map((l) => l.trim()).filter((l) => l)) {
  const parts = line.split('=').map((p) => p.trim());
  if (parts.length !== 2 || !core.SAFE_ID.test(parts[0]) || !core.SAFE_ID.test(parts[1]) || Object.prototype.hasOwnProperty.call(mappings, parts[0])) {
    return fail('Mapping "' + line.slice(0, 80) + '" must be a unique sourceId=targetId.');
  }
  mappings[parts[0]] = parts[1];
}
let cfg;
try { cfg = core.gitlabConfig(params.gitlabUrl, params.gitlabProjectId, params.gitlabEnvironment); } catch (e) { return fail('GitLab settings: ' + e.message); }
try {
  const id = core.randomHexId(String(params.userId || '') + '|' + selected.join(','));
  const built = core.newRequest(core.parseBody(params.source), core.parseBody(params.target), String(params.sourceRegion || ''),
    String(params.targetRegion || ''), selected, mappings, String(params.description || ''), id, core.rfc3339Nano(new Date()));
  const r = built.request;
  const summary = 'Request ' + r.id + ': ' + r.selected.length + ' resource(s) from ' + r.source.id + ' (snapshot ' + r.source.snapshotId +
    ') to ' + r.target.id + '. Submitting opens a merge request in GitLab project ' + cfg.projectId + '.';
  return { state: 'ok', error: '', requestId: r.id, requestJson: built.json, gitlabApi: cfg.api,
    previewText: core.escapeHtml(built.json), summaryText: core.escapeHtml(summary) };
} catch (e) {
  return fail(String(e.message || e));
}
