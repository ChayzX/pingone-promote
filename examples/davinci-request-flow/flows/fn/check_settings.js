// First step: check the flow variables an admin sets, so a placeholder or a
// typo is reported by name instead of as an API error later.
const GITLAB_ENV = /^[A-Za-z0-9][A-Za-z0-9_-]{0,255}$/;
const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;
const problems = [];
const origin = (name, host, value) => {
  const v = String(value || '');
  if (!new RegExp('^https://' + host + '\\.pingone\\.(com|eu|ca|com\\.au|sg|asia)$').test(v)) {
    problems.push(name + ' must be like https://' + host + '.pingone.com (no trailing slash); it is "' + v.slice(0, 60) + '"');
  }
};
origin('apiOrigin', 'api', params.apiOrigin);
origin('authOrigin', 'auth', params.authOrigin);
if (!UUID.test(String(params.hostEnvironmentId || '').trim())) {
  problems.push('hostEnvironmentId must be the ID of the environment where the worker app lives; it is "' + String(params.hostEnvironmentId || '').slice(0, 60) + '"');
}
const slots = [
  [1, params.env1Name, params.env1Id, params.env1GitlabEnvironment], [2, params.env2Name, params.env2Id, params.env2GitlabEnvironment],
  [3, params.env3Name, params.env3Id, params.env3GitlabEnvironment], [4, params.env4Name, params.env4Id, params.env4GitlabEnvironment]
];
let configured = 0;
for (const [n, name, id, gitlabEnv] of slots) {
  const nm = String(name || '').trim();
  const ident = String(id || '').trim();
  if (!nm && !ident) { continue; }
  configured++;
  if (!nm) { problems.push('env' + n + 'Name is empty but env' + n + 'Id is set'); }
  if (!UUID.test(ident)) { problems.push('env' + n + 'Id must be an environment ID; it is "' + ident.slice(0, 60) + '"'); }
  const g = String(gitlabEnv || '').trim();
  if (g && !GITLAB_ENV.test(g)) { problems.push('env' + n + 'GitlabEnvironment must be a GitLab environment name like dev or production; it is "' + g.slice(0, 60) + '"'); }
}
if (configured < 2) { problems.push('Set at least two environments (env1Name/env1Id and env2Name/env2Id)'); }
const project = String(params.gitlabProjectId || '').trim();
if (!/^[0-9]+$/.test(project)) { problems.push('gitlabProjectId must be the numeric ID of the promotion project; it is "' + project.slice(0, 60) + '"'); }
const defEnv = String(params.gitlabEnvironment || '').trim();
if (!GITLAB_ENV.test(defEnv)) { problems.push('gitlabEnvironment must be a GitLab environment name like production; it is "' + defEnv.slice(0, 60) + '"'); }
if (!/^https:\/\/[^/?#\s]+/.test(String(params.gitlabUrl || ''))) { problems.push('gitlabUrl must be an HTTPS URL such as https://gitlab.com'); }
if (problems.length) {
  return { state: 'error', error: core.escapeHtml('Fix these flow variables, then run again: ' + problems.join('; ') + '.') };
}
return { state: 'ok', error: '' };
