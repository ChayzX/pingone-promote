// VerifyProject, second half (default branch must be protected), then the
// three Submit payloads built from the request exactly as the Go app does.
const fail = (m) => ({ state: 'error', error: core.escapeHtml(m), branch: '', branchBody: '', commitBody: '', mrBody: '' });
try {
  const defaultBranch = String(params.defaultBranch || '');
  core.checkDefaultBranch(defaultBranch, params.statusCode, params.body);
  const cfg = core.gitlabConfig(params.gitlabUrl, params.gitlabProjectId, params.gitlabEnvironment);
  const json = String(params.requestJson || '');
  const request = JSON.parse(json);
  core.validateRequest(request);
  if (core.encodeRequest(request) !== json) { return fail('The request changed after it was built; start again.'); }
  // Shown in the merge request only; request.json stays byte-identical to Go's.
  const who = String(params.username || '').replace(/[`\r\n]/g, '').slice(0, 200);
  const uid = core.SAFE_ID.test(String(params.userId || '')) ? String(params.userId) : '';
  const s = core.submitBodies(request, json, cfg.environment, uid ? who + ' (PingOne user ' + uid + ')' : '',
    String(params.variablesMd || ''));
  return { state: 'ok', error: '', branch: s.branch, branchBody: s.branchBody(defaultBranch), commitBody: s.commitBody(defaultBranch),
    mrBody: s.mrBody(defaultBranch) };
} catch (e) {
  return fail(String(e.message || e));
}
