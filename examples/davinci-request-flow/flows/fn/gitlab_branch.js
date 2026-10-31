// VerifyProject, second half (default branch must be protected), then the
// three Submit payloads for the intent: the chosen/default branch, a commit
// that adds intents/<id>/intent.json, and the merge request.
const fail = (m) => ({ state: 'error', error: core.escapeHtml(m), branch: '', branchBody: '', commitBody: '', mrBody: '' });
try {
  const defaultBranch = String(params.defaultBranch || '');
  core.checkDefaultBranch(defaultBranch, params.statusCode, params.body);
  const cfg = core.gitlabConfig(params.gitlabUrl, params.gitlabProjectId, params.gitlabEnvironment);
  const json = String(params.intentJson || '');
  const intent = JSON.parse(json);
  core.validateIntent(intent);
  if (JSON.stringify(intent, null, 2) + '\n' !== json) { return fail('The intent changed after it was built; start again.'); }
  const s = core.intentBodies(intent, json, cfg.environment, { source: String(params.sourceEnvName || ''), target: String(params.targetEnvName || '') }, '',
    { branchName: String(params.branchName || ''), commitMessage: String(params.commitMessage || '') });
  return { state: 'ok', error: '', branch: s.branch, branchBody: s.branchBody(defaultBranch), commitBody: s.commitBody(defaultBranch),
    mrBody: s.mrBody(defaultBranch) };
} catch (e) {
  return fail(String(e.message || e));
}
