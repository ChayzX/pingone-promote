// Before the start page: report which settings are missing or malformed (names only, never values).
const r = lib.checkSettings(params);
const rows = lib.SETTINGS.map((n) => {
  const p = r.problems.filter((x) => x.name === n)[0];
  const status = p ? p.issue : (lib.str(params[n]) ? 'set' : (lib.OPTIONAL.indexOf(n) >= 0 ? 'default / not used' : 'not set'));
  return '<tr><td><code>' + lib.escapeHtml(lib.varName(n)) + '</code></td><td class="' + (p ? 'bad' : 'ok') + '">' + lib.escapeHtml(status) + '</td></tr>';
}).join('');
const gitlabReady = !!(r.cfg.gitlabApi && r.cfg.gitlabProjectId);
return {
  ready: r.problems.length ? 'no' : 'yes',
  statusHtml: '<table class="fs-table"><thead><tr><th>Flow variable</th><th>Status</th></tr></thead><tbody>' + rows + '</tbody></table>' +
    (r.problems.length ? '<p class="fs-note bad">Fix the settings marked in red (Flow &rarr; Variables), deploy, then launch again.</p>'
      : '<p class="fs-note ok">Settings look complete. GitLab write: ' + (gitlabReady ? 'configured' : 'not configured, so stage 5 is skipped') + '.</p>')
};
