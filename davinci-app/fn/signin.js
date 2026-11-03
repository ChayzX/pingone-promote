// After the PingOne SSO sign-in subflow: check who signed in, check the
// settings, and start the session state. Nothing else runs before this passes.
const s = lib.state({});
s.v = lib.VERSION;
s.startedAt = lib.now();
const r = lib.checkSettings(params);
const clean = (v, max) => lib.str(v).replace(/[\u0000-\u001f\u007f{}<>"\\]/g, '').slice(0, max);
const user = { id: clean(params.userId, 64), username: clean(params.username, 120), name: clean(params.name, 120) };
let groups = lib.parse(params.groups);
if (typeof groups === 'string') { groups = groups.split(','); }
groups = (Array.isArray(groups) ? groups : []).map((g) => String(g && typeof g === 'object' ? g.name || '' : g).trim()).filter((g) => g);
const stop = (title, lines) => {
  const items = lines.map((l) => '<li>' + lib.esc(l) + '</li>').join('');
  return lib.out(s, 'stop', { html: '<h1 data-pp-focus>' + lib.esc(title) + '</h1><ul class="pp-list">' + items + '</ul>' });
};
if (!user.id || !user.username) {
  return stop('Sign-in required', ['PingOne Promote opens only after a PingOne sign-in. The sign-in step did not return a user. Close this tab and start again.']);
}
if (r.problems.length) {
  // Setting names and what is wrong; never their values.
  return stop('PingOne Promote is not set up yet', ['Ask your DaVinci administrator to fix these flow variables:']
    .concat(r.problems.map((p) => lib.varName(p.name) + ': ' + p.issue)));
}
const allowed = r.cfg.allowedGroups;
if (allowed[0] !== '*' && !groups.some((g) => allowed.indexOf(g) >= 0)) {
  return stop('You do not have access to PingOne Promote', ['You are signed in as ' + user.username + ', but you are not in a group that may use this app. Ask your PingOne administrator for access.']);
}
s.user = user;
s.cfg = r.cfg;
s.screen = 'search';
s.search = { source: Object.keys(r.cfg.sources)[0], query: '', matches: null, total: 0 };
s.sub = { state: 'idle' };
s.errors = {};
return lib.out(s, 'ok', { html: '' });
