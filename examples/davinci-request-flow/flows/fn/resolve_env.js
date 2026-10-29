// Turn an environment button (ENV1..ENV4) into that environment's configured
// ID and name. Refuses an empty slot, an unsafe ID, and the excluded ID (the
// source, when choosing the target).
const choice = String(params.choice || '');
const m = /^ENV([1-4])$/.exec(choice);
const fail = (msg) => ({ state: 'error', error: core.escapeHtml(msg), environmentId: '', name: '', nameText: '', gitlabEnvironment: '' });
if (!m) { return fail('Choose one of the listed environments.'); }
const slots = {
  1: [params.env1Id, params.env1Name, params.env1GitlabEnvironment], 2: [params.env2Id, params.env2Name, params.env2GitlabEnvironment],
  3: [params.env3Id, params.env3Name, params.env3GitlabEnvironment], 4: [params.env4Id, params.env4Name, params.env4GitlabEnvironment]
};
const id = String(slots[m[1]][0] || '').trim();
const name = String(slots[m[1]][1] || '').trim().slice(0, 100);
if (!id || !name) { return fail('That environment slot is not configured. Set env' + m[1] + 'Name and env' + m[1] + 'Id in the flow variables.'); }
if (!core.SAFE_ID.test(id)) { return fail('env' + m[1] + 'Id is not a valid environment ID.'); }
if (id === String(params.excludeId || '')) { return fail('Source and target must be different environments.'); }
// The GitLab environment (in the one promotion project) that deploys to this
// environment as a target: the slot's own, else gitlabEnvironment.
const gitlabEnvironment = String(slots[m[1]][2] || '').trim() || String(params.defaultGitlabEnvironment || '').trim();
if (!/^[A-Za-z0-9][A-Za-z0-9_-]{0,255}$/.test(gitlabEnvironment)) {
  return fail('The GitLab environment for ' + name + ' must be a name like dev, test or production (env' + m[1] + 'GitlabEnvironment or gitlabEnvironment).');
}
return { state: 'ok', error: '', environmentId: id, name: name, nameText: core.escapeHtml(name), gitlabEnvironment: gitlabEnvironment };
