// Turn an environment button (ENV1..ENV4) into that environment's configured
// ID and name. Refuses an empty slot, an unsafe ID, and the excluded ID (the
// source, when choosing the target).
const choice = String(params.choice || '');
const m = /^ENV([1-4])$/.exec(choice);
const fail = (msg) => ({ state: 'error', error: core.escapeHtml(msg), environmentId: '', name: '', nameText: '', gitlabProjectId: '' });
if (!m) { return fail('Choose one of the listed environments.'); }
const slots = {
  1: [params.env1Id, params.env1Name, params.env1GitlabProjectId], 2: [params.env2Id, params.env2Name, params.env2GitlabProjectId],
  3: [params.env3Id, params.env3Name, params.env3GitlabProjectId], 4: [params.env4Id, params.env4Name, params.env4GitlabProjectId]
};
const id = String(slots[m[1]][0] || '').trim();
const name = String(slots[m[1]][1] || '').trim().slice(0, 100);
if (!id || !name) { return fail('That environment slot is not configured. Set env' + m[1] + 'Name and env' + m[1] + 'Id in the flow variables.'); }
if (!core.SAFE_ID.test(id)) { return fail('env' + m[1] + 'Id is not a valid environment ID.'); }
if (id === String(params.excludeId || '')) { return fail('Source and target must be different environments.'); }
// The GitLab project whose pipeline deploys to this environment (as a target):
// the slot's own project, else the default project.
const project = String(slots[m[1]][2] || '').trim() || String(params.defaultGitlabProjectId || '').trim();
if (params.needProject === 'true' && !project) {
  return fail('No GitLab project is set up for ' + name + '. Set env' + m[1] + 'GitlabProjectId (or gitlabProjectId) to the project whose pipeline deploys to it.');
}
if (params.needProject === 'true' && !/^[0-9]+$/.test(project)) {
  return fail('The GitLab project ID for ' + name + ' must be numeric.');
}
return { state: 'ok', error: '', environmentId: id, name: name, nameText: core.escapeHtml(name), gitlabProjectId: project };
