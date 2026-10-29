'use strict';
// Exercises the DaVinci custom functions in Node and validates the generated
// intent with the real Go runner (`pingone-runner validate --intent`).
const assert = require('assert');
const fs = require('fs');
const os = require('os');
const path = require('path');
const { execFileSync } = require('child_process');

const dir = path.join(__dirname, 'functions');
const load = (name) => require(path.join(dir, name));
const authz = load('authz.js');
const listapps = load('listapps.js');
const build = load('buildintent.js');
const result = load('result.js');
const runner = process.argv[2];

const ORG = 'org-0000';
const SRC = 'source-env-0000';
const TGT = 'target-env-0000';
const ROLE = 'davinci-admin-role-id';
const ra = (roleId, type, id) => ({ role: { id: roleId }, scope: { type, id } });
const roles = (...list) => ({ _embedded: { roleAssignments: list } });
const userinfo = { sub: 'user-123', preferred_username: 'chase@example.test' };

async function main() {
  let passed = 0;
  const ok = (cond, msg) => { assert.ok(cond, msg); passed++; };
  const base = { userinfoStatus: 200, userinfoBody: userinfo, roleAssignmentsStatus: 200, davinciAdminRoleId: ROLE, organizationId: ORG, sourceEnvironmentId: SRC };

  // authz
  ok((await authz({ params: { ...base, roleAssignmentsBody: roles(ra(ROLE, 'ORGANIZATION', ORG)) } })).decision === 'allow', 'org-scoped DaVinci Admin allowed');
  ok((await authz({ params: { ...base, roleAssignmentsBody: JSON.stringify(roles(ra(ROLE, 'ENVIRONMENT', SRC))) } })).decision === 'allow', 'source-env DaVinci Admin allowed (string body)');
  ok((await authz({ params: { ...base, roleAssignmentsBody: roles(ra(ROLE, 'ENVIRONMENT', TGT)) } })).decision === 'deny', 'DaVinci Admin on another env denied');
  ok((await authz({ params: { ...base, roleAssignmentsBody: roles(ra('other-role', 'ORGANIZATION', ORG)) } })).decision === 'deny', 'other role denied');
  ok((await authz({ params: { ...base, roleAssignmentsBody: roles(ra(ROLE, 'ORGANIZATION', 'other-org')) } })).decision === 'deny', 'other org denied');
  ok((await authz({ params: { ...base, roleAssignmentsBody: roles() } })).decision === 'deny', 'no roles denied');
  ok((await authz({ params: { ...base, userinfoStatus: 401, roleAssignmentsBody: roles(ra(ROLE, 'ORGANIZATION', ORG)) } })).decision === 'deny', 'failed userinfo denied');
  ok((await authz({ params: { ...base, roleAssignmentsStatus: 403, roleAssignmentsBody: roles(ra(ROLE, 'ORGANIZATION', ORG)) } })).decision === 'deny', 'unreadable roles denied');
  ok((await authz({ params: { ...base, userinfoBody: { sub: '../x' }, roleAssignmentsBody: roles(ra(ROLE, 'ORGANIZATION', ORG)) } })).decision === 'deny', 'unsafe sub denied');
  ok((await authz({ params: { ...base, davinciAdminRoleId: '', roleAssignmentsBody: roles(ra('', 'ORGANIZATION', ORG)) } })).decision === 'deny', 'unconfigured role ID denied (fails closed)');
  // Role ID looked up by name when no override is set.
  const roleList = { _embedded: { roles: [{ id: ROLE, name: 'DaVinci Admin' }, { id: 'ro', name: 'DaVinci Admin Read Only' }] } };
  ok((await authz({ params: { ...base, davinciAdminRoleId: '', rolesListStatus: 200, rolesListBody: roleList, roleAssignmentsBody: roles(ra(ROLE, 'ORGANIZATION', ORG)) } })).decision === 'allow', 'role found by exact name');
  ok((await authz({ params: { ...base, davinciAdminRoleId: '', rolesListStatus: 200, rolesListBody: roleList, roleAssignmentsBody: roles(ra('ro', 'ORGANIZATION', ORG)) } })).decision === 'deny', 'Read Only role is not DaVinci Admin');
  ok((await authz({ params: { ...base, davinciAdminRoleId: '', rolesListStatus: 403, rolesListBody: {}, roleAssignmentsBody: roles(ra(ROLE, 'ORGANIZATION', ORG)) } })).decision === 'deny', 'role list unreadable and no override: denied');
  const allowed = await authz({ params: { ...base, roleAssignmentsBody: roles(ra(ROLE, 'ORGANIZATION', ORG)) } });
  ok(allowed.userId === 'user-123' && allowed.username === 'chase@example.test', 'identity from userinfo');
  const sneaky = await authz({ params: { ...base, userinfoBody: { sub: 'user-9', preferred_username: '<script>x</script>' }, roleAssignmentsBody: roles(ra(ROLE, 'ORGANIZATION', ORG)) } });
  ok(sneaky.usernameText === '&lt;script&gt;x&lt;/script&gt;', 'display name is HTML-escaped');

  // listapps escapes names
  const apps = await listapps({ params: { applicationsStatus: 200, applicationsBody: { _embedded: { applications: [{ id: 'app-2', name: 'Zeta' }, { id: 'app-1', name: '<img src=x onerror=alert(1)>' }] } } } });
  ok(apps.count === 2 && !apps.listText.includes('<img') && apps.listText.includes('&lt;img') && apps.listText.split('\n')[0].startsWith('app-1'), 'application names are HTML-escaped, sorted by name');
  ok((await listapps({ params: { applicationsStatus: 403, applicationsBody: {} } })).count === 0, 'listing failure handled');

  // buildintent
  const good = { userId: 'user-123', username: 'chase@example.test', sourceEnvironmentId: SRC, targetEnvironmentId: TGT, region: 'us', selectedRaw: 'app-1, app-2\nflow-9', mappingsRaw: 'app-1=prod-app-1\n', description: 'Promote login <b>changes</b>', defaultBranch: 'main' };
  const b = await build({ params: good });
  ok(b.status === 'ok', 'valid intent built: ' + b.error);
  ok(/^[a-f0-9]{32}$/.test(b.intentId), 'intent ID is 32 lowercase hex');
  ok(!b.summaryText.includes('<b>changes') && b.summaryText.includes('&lt;b&gt;changes'), 'summary escapes description');
  const commit = JSON.parse(b.commitBody);
  ok(commit.actions[0].file_path === 'intents/' + b.intentId + '/intent.json' && commit.branch === 'front-door/' + b.intentId && commit.start_branch === 'main', 'commit targets intents/<id>/intent.json on a new branch');
  const mr = JSON.parse(b.mrBody);
  ok(mr.target_branch === 'main' && mr.source_branch === commit.branch, 'merge request goes from branch to main');
  for (const [name, patch] of [
    ['empty selection', { selectedRaw: ' , ' }],
    ['traversal ID', { selectedRaw: '../etc' }],
    ['duplicate ID', { selectedRaw: 'a1 a1' }],
    ['bad mapping', { mappingsRaw: 'a1' }],
    ['conflicting mapping', { mappingsRaw: 'a1=t1\na2=t1' }],
    ['long description', { description: 'x'.repeat(3001) }],
    ['control chars', { description: 'bad\u0007bell' }],
    ['same env', { targetEnvironmentId: SRC }],
    ['bad region', { region: 'moon' }],
    ['no identity', { userId: '' }]
  ]) {
    ok((await build({ params: { ...good, ...patch } })).status === 'invalid', 'rejected: ' + name);
  }

  // The real runner must accept the generated intent at its committed path.
  if (runner) {
    const root = fs.mkdtempSync(path.join(os.tmpdir(), 'fd-'));
    const p = path.join(root, 'intents', b.intentId, 'intent.json');
    fs.mkdirSync(path.dirname(p), { recursive: true });
    fs.writeFileSync(p, commit.actions[0].content);
    const out = execFileSync(runner, ['validate', '--intent', p]).toString().trim();
    ok(out === 'valid', 'pingone-runner validate --intent accepts the generated intent');
  }

  // result
  ok((await result({ params: { commitStatus: 201, mrStatus: 201, mrBody: { web_url: 'https://gitlab.com/x/-/merge_requests/1' } } })).result === 'ok', 'success detected');
  ok((await result({ params: { commitStatus: 400, mrStatus: 0, mrBody: {} } })).result === 'failed', 'commit failure detected');
  ok((await result({ params: { commitStatus: 201, mrStatus: 409, mrBody: '{}' } })).result === 'failed', 'MR failure detected');
  ok((await result({ params: { commitStatus: 201, mrStatus: 201, mrBody: { web_url: 'javascript:alert(1)' } } })).result === 'failed', 'non-https MR URL rejected');

  console.log('all ' + passed + ' checks passed');
}
main().catch((e) => { console.error(e.message); process.exit(1); });
