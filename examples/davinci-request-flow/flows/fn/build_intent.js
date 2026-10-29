// Turn the picker's choices into intents/<id>/intent.json, validated with the
// runner's rules (gitops.ValidateIntent). The intent carries no configuration:
// after the merge, the protected plan job runs `pingone-runner build-request`
// to build the redacted, fingerprinted request.json from fresh snapshots.
// Also checks the GitLab settings, so every GitLab URL used later is checked.
const fail = (m) => ({ state: 'error', error: core.escapeHtml(m), intentId: '', intentJson: '', gitlabApi: '', previewText: '', summaryText: '' });
const selected = String(params.selectedRaw || '').split(/[\s,]+/).filter((s) => s.length > 0);
if (selected.length === 0) { return fail('Select at least one resource.'); }
for (const id of selected) {
  if (!core.SAFE_ID.test(id)) { return fail('Resource ID "' + id.slice(0, 80) + '" is not a valid ID.'); }
}
const mappings = {};
const mappedTo = {};
for (const line of String(params.mappingsRaw || '').split(/\r?\n/).map((l) => l.trim()).filter((l) => l)) {
  const parts = line.split('=').map((p) => p.trim());
  if (parts.length !== 2 || !core.SAFE_ID.test(parts[0]) || !core.SAFE_ID.test(parts[1]) || Object.prototype.hasOwnProperty.call(mappings, parts[0])) {
    return fail('Mapping "' + line.slice(0, 80) + '" must be a unique sourceId=targetId.');
  }
  if (mappedTo[parts[1]]) { return fail('Two resources are mapped to the same destination ' + parts[1] + '. Choose a different destination for one of them.'); }
  mappings[parts[0]] = parts[1];
  mappedTo[parts[1]] = true;
}
// Browsers submit textarea line breaks as \r\n; the intent allows \n only.
const description = String(params.description || '').replace(/\r\n?/g, '\n').trim();
// The same selection and mapping rules the runner applies when it builds the request
// (promotion.BuildRequest), checked now against the live inventories (names only).
try {
  core.buildRequest(core.parseBody(params.sourceInventory), core.parseBody(params.targetInventory), selected, mappings, description);
} catch (e) { return fail(String(e.message || e)); }
let cfg;
try { cfg = core.gitlabConfig(params.gitlabUrl, params.gitlabProjectId, params.gitlabEnvironment); } catch (e) { return fail('GitLab settings: ' + e.message); }
try {
  const id = core.randomHexId(String(params.requesterId || '') + '|' + selected.join(','));
  const built = core.newIntent(id, { userId: String(params.requesterId || ''), username: String(params.requesterName || '') },
    { id: String(params.sourceEnvironmentId || ''), region: String(params.sourceRegion || '') },
    { id: String(params.targetEnvironmentId || ''), region: String(params.targetRegion || '') }, selected, mappings, description);
  const summary = 'Intent ' + id + ': ' + selected.length + ' resource(s), ' + Object.keys(mappings).length + ' mapping(s). After the merge request is merged, ' +
    'the GitLab plan job builds the full request from fresh snapshots and plans it for review in project ' + cfg.projectId + '.';
  return { state: 'ok', error: '', intentId: id, intentJson: built.json, gitlabApi: cfg.api, previewText: core.escapeHtml(built.json),
    summaryText: core.escapeHtml(summary) };
} catch (e) {
  return fail(String(e.message || e));
}
