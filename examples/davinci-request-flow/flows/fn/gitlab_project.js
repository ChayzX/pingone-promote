// VerifyProject, first half: the project must be the configured one, private,
// active, with a default branch, and its web URL on the configured server path.
try {
  const cfg = core.gitlabConfig(params.gitlabUrl, params.gitlabProjectId, params.gitlabEnvironment);
  const p = core.checkProject(cfg, params.statusCode, params.body);
  return { state: 'ok', error: '', defaultBranch: p.defaultBranch, defaultBranchPath: p.defaultBranchPath };
} catch (e) {
  return { state: 'error', error: core.escapeHtml(String(e.message || e)), defaultBranch: '', defaultBranchPath: '' };
}
