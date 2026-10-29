module.exports = a = async ({params}) => {
  // Build intents/<id>/intent.json exactly as `pingone-runner validate --intent`
  // expects, plus the GitLab commit and merge request bodies. The requester
  // comes from the verified sign-in, never from the form.
  const safe = /^[A-Za-z0-9][A-Za-z0-9_-]{0,255}$/;
  const regions = ['us', 'eu', 'ca', 'au', 'sg', 'asia'];
  const esc = (s) => String(s).replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
  const fail = (msg) => ({ status: 'invalid', error: msg, intentId: '', intentJson: '', commitBody: '', mrBody: '', summaryText: '' });
  // eslint-disable-next-line no-control-regex
  const hasControl = (s, allowNewlines) => (allowNewlines ? /[\u0000-\u0008\u000B\u000C\u000E-\u001F\u007F]/ : /[\u0000-\u001F\u007F]/).test(s);

  const userId = String(params.userId || '');
  const username = String(params.username || '');
  if (!safe.test(userId) || !username || username.length > 256 || hasControl(username, false)) {
    return fail('Your sign-in identity is missing or invalid; sign in again.');
  }
  const source = { id: String(params.sourceEnvironmentId || ''), region: String(params.region || '') };
  const target = { id: String(params.targetEnvironmentId || ''), region: String(params.region || '') };
  if (!safe.test(source.id) || !safe.test(target.id) || regions.indexOf(source.region) < 0 || source.id === target.id) {
    return fail('The front door has an invalid source/target environment configuration.');
  }

  const selected = String(params.selectedRaw || '').split(/[\s,]+/).filter((s) => s.length > 0);
  if (selected.length === 0) { return fail('Select at least one resource ID.'); }
  if (selected.length > 10000) { return fail('Select at most 10000 resources.'); }
  const seen = {};
  for (const id of selected) {
    if (!safe.test(id)) { return fail('Resource ID "' + id.slice(0, 80) + '" is not a valid ID.'); }
    if (seen[id]) { return fail('Resource ID "' + id + '" is listed twice.'); }
    seen[id] = true;
  }

  const mappings = {};
  const mappedTargets = {};
  const lines = String(params.mappingsRaw || '').split(/\r?\n/).map((l) => l.trim()).filter((l) => l.length > 0);
  for (const line of lines) {
    const parts = line.split('=').map((p) => p.trim());
    if (parts.length !== 2 || !safe.test(parts[0]) || !safe.test(parts[1])) {
      return fail('Mapping "' + line.slice(0, 80) + '" must be sourceId=targetId.');
    }
    if (Object.prototype.hasOwnProperty.call(mappings, parts[0]) || mappedTargets[parts[1]]) {
      return fail('Mapping "' + line.slice(0, 80) + '" repeats a source or target ID.');
    }
    mappings[parts[0]] = parts[1];
    mappedTargets[parts[1]] = true;
  }

  const description = String(params.description || '').trim();
  if (description.length > 3000 || hasControl(description, true)) {
    return fail('The description must be at most 3000 characters of plain text.');
  }

  let id = '';
  for (let i = 0; i < 32; i++) { id += Math.floor(Math.random() * 16).toString(16); }

  const intent = {
    version: 1,
    id: id,
    requestedBy: { pingoneUserId: userId, username: username },
    source: source,
    target: target,
    selected: selected,
    mappings: mappings,
    description: description
  };
  const intentJson = JSON.stringify(intent, null, 2) + '\n';
  const branch = 'front-door/' + id;
  const path = 'intents/' + id + '/intent.json';
  const commitBody = JSON.stringify({
    branch: branch,
    start_branch: String(params.defaultBranch || 'main'),
    commit_message: 'Promotion intent ' + id + ' requested by ' + username,
    actions: [{ action: 'create', file_path: path, content: intentJson }]
  });
  const mrDescription = [
    'Promotion intent submitted through the DaVinci front door.',
    '',
    '- Requested by: ' + username + ' (PingOne user ' + userId + ')',
    '- Source: ' + source.id + ' (' + source.region + ')',
    '- Target: ' + target.id + ' (' + target.region + ')',
    '- Selected: ' + selected.length + ' resource(s)',
    '',
    'Review `' + path + '`. After merge, the protected pipeline builds the full request, plans it, and waits for deployment approval.',
    description ? '\n> ' + description.replace(/\n/g, '\n> ') : ''
  ].join('\n');
  const mrBody = JSON.stringify({
    source_branch: branch,
    target_branch: String(params.defaultBranch || 'main'),
    title: 'Promotion intent ' + id + ' (' + username + ')',
    description: mrDescription,
    remove_source_branch: true
  });
  const summaryText = esc('Requested by ' + username + '\n' + source.id + ' -> ' + target.id + ' (' + source.region + ')\n\n' + intentJson);
  return { status: 'ok', error: '', intentId: id, intentJson: intentJson, commitBody: commitBody, mrBody: mrBody, summaryText: summaryText };
};
