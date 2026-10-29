// A GitLab write (create branch, commit) must return 2xx, like Go's Client.Do.
try {
  core.gitlabStatus(params.statusCode, String(params.what || 'GitLab request'));
  return { state: 'ok', error: '' };
} catch (e) {
  return { state: 'error', error: core.escapeHtml(String(e.message || e)) };
}
