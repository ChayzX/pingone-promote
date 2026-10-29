module.exports = a = async ({params}) => {
  // Decide whether the signed-in PingOne admin may use the front door: they need
  // the DaVinci Admin role scoped to the whole organization or to the source
  // environment. Inputs come from the userinfo and roleAssignments API calls.
  const parse = (v) => {
    if (v === null || v === undefined) { return {}; }
    if (typeof v === 'string') { try { return JSON.parse(v); } catch (e) { return {}; } }
    return v;
  };
  const safe = /^[A-Za-z0-9][A-Za-z0-9_-]{0,255}$/;
  const deny = (reason) => ({ decision: 'deny', reason: reason, userId: '', username: '', usernameText: '' });
  const esc = (v) => String(v).replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));

  const userinfo = parse(params.userinfoBody);
  const roles = parse(params.roleAssignmentsBody);
  const userId = String(userinfo.sub || '');
  const username = String(userinfo.preferred_username || userinfo.email || userinfo.sub || '');
  if (Number(params.userinfoStatus) !== 200 || !safe.test(userId)) {
    return deny('Sign-in could not be verified.');
  }
  if (Number(params.roleAssignmentsStatus) !== 200) {
    return deny('Role assignments could not be read.');
  }
  // The DaVinci Admin role ID comes from the optional override variable, or is
  // looked up by its exact name in PingOne's role list.
  let roleId = String(params.davinciAdminRoleId || '');
  if (!roleId && Number(params.rolesListStatus) === 200) {
    const all = ((parse(params.rolesListBody)._embedded || {}).roles) || [];
    const match = all.filter((r) => r && r.name === 'DaVinci Admin' && typeof r.id === 'string');
    if (match.length === 1) { roleId = match[0].id; }
  }
  const orgId = String(params.organizationId || '');
  const sourceEnvId = String(params.sourceEnvironmentId || '');
  if (!roleId || !orgId || !sourceEnvId) {
    return deny('Front door is not configured (role, organization or source environment missing).');
  }
  const list = ((roles._embedded || {}).roleAssignments) || [];
  const allowed = list.some((ra) => {
    const r = (ra && ra.role) || {};
    const s = (ra && ra.scope) || {};
    if (r.id !== roleId) { return false; }
    return (s.type === 'ORGANIZATION' && s.id === orgId) || (s.type === 'ENVIRONMENT' && s.id === sourceEnvId);
  });
  if (!allowed) {
    return deny('You need the DaVinci Admin role for this organization or for the source environment.');
  }
  return { decision: 'allow', reason: '', userId: userId, username: username.slice(0, 256), usernameText: esc(username.slice(0, 256)) };
};
