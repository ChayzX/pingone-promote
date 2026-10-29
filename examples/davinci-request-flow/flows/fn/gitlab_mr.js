// The merge request must exist and its URL must be on the configured server path.
try {
  const cfg = core.gitlabConfig(params.gitlabUrl, params.gitlabProjectId, params.gitlabEnvironment);
  const mr = core.checkMergeRequest(cfg, params.statusCode, params.body);
  return { state: 'ok', error: '', webUrl: mr.webUrl, webUrlText: core.escapeHtml(mr.webUrl), iid: mr.iid };
} catch (e) {
  return { state: 'error', error: core.escapeHtml(String(e.message || e)), webUrl: '', webUrlText: '', iid: 0 };
}
