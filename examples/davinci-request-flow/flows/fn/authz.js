// Sign-in check: verified userinfo + DaVinci Admin on the organization or the
// source environment. Fails closed on any unreadable or unconfigured input.
const deny = (reason) => ({ decision: 'deny', reason: reason, userId: '', username: '', usernameText: '' });
const userinfo = core.parseBody(params.userinfoBody) || {};
const roles = core.parseBody(params.roleAssignmentsBody) || {};
const userId = String(userinfo.sub || '');
const username = String(userinfo.preferred_username || userinfo.email || userinfo.sub || '').slice(0, 256);
if (Number(params.userinfoStatus) !== 200 || !core.SAFE_ID.test(userId)) { return deny('Sign-in could not be verified.'); }
if (Number(params.roleAssignmentsStatus) !== 200) { return deny('Role assignments could not be read.'); }
let roleId = String(params.davinciAdminRoleId || '');
if (!roleId && Number(params.rolesListStatus) === 200) {
  const all = (((core.parseBody(params.rolesListBody) || {})._embedded || {}).roles) || [];
  const match = all.filter((r) => r && r.name === 'DaVinci Admin' && typeof r.id === 'string');
  if (match.length === 1) { roleId = match[0].id; }
}
const orgId = String(params.organizationId || '');
const sourceEnvId = String(params.sourceEnvironmentId || '');
if (!roleId || !orgId || !sourceEnvId) { return deny('Not configured: DaVinci Admin role, organization or source environment is missing.'); }
const list = (roles._embedded || {}).roleAssignments || [];
const allowed = list.some((ra) => {
  const r = (ra && ra.role) || {};
  const s = (ra && ra.scope) || {};
  return r.id === roleId && ((s.type === 'ORGANIZATION' && s.id === orgId) || (s.type === 'ENVIRONMENT' && s.id === sourceEnvId));
});
if (!allowed) { return deny('You need the DaVinci Admin role for this organization or for the source environment.'); }
return { decision: 'allow', reason: '', userId: userId, username: username, usernameText: core.escapeHtml(username) };
