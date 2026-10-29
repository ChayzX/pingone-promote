// Unit tests for core.js pieces without a direct Go counterpart.
const assert = require('assert');
const crypto = require('crypto');
const core = require('./load_core');

let n = 0;
const ok = (c, m) => { assert.ok(c, m); n++; };

// SHA-256 against Node's implementation, including multi-block and UTF-8 input.
for (const s of ['', 'abc', 'a'.repeat(55), 'a'.repeat(56), 'a'.repeat(64), 'a'.repeat(1000), 'héllo ✓ 𝄞', JSON.stringify({ x: [1, 2, { y: 'z' }] })]) {
  ok(core.sha256(s) === crypto.createHash('sha256').update(s, 'utf8').digest('hex'), 'sha256 matches for length ' + s.length);
}
ok(core.canonical({ b: 1, a: [{ d: 2, c: 3 }] }) === '{"a":[{"c":3,"d":2}],"b":1}', 'canonical sorts keys at every level');

// apiURL
const O = 'https://api.pingone.com';
ok(core.apiURL(O, O + '/v1/environments/e/flows/f').path === '/v1/environments/e/flows/f', 'apiURL accepts env path');
ok(core.apiURL(O, '/v1/environments/e/x').path === '/v1/environments/e/x', 'apiURL resolves absolute path');
for (const bad of ['https://evil.example/v1/environments/e', 'http://api.pingone.com/v1/environments/e', O + '/v2/x', O + '/v1/environments/e/a%20b',
  O + '/v1/environments/e/a b', 'https://u@api.pingone.com/v1/environments/e', O + '/v1/environments/e#f', O + '/v1/environments/../../x', '//evil.example/v1/environments/e']) {
  ok(core.apiURL(O, bad) === null, 'apiURL refuses ' + bad);
}

// GitLab: deliberate deviation from Go. Go accepts a backslash in the base
// URL; URL parsers in HTTP clients may read it as "/", so the port refuses it.
assert.throws(() => core.gitlabConfig('https://gitlab.example.com/a\\b', '42', ''));
n++;
const cfg = core.gitlabConfig('https://gitlab.example.com/tools/', '42', '');
ok(cfg.api === 'https://gitlab.example.com/tools/api/v4/projects/42' && cfg.environment === 'production', 'GitLab API base and default environment');
const project = { id: 42, visibility: 'private', archived: false, default_branch: 'release/main', web_url: 'https://gitlab.example.com/tools/p' };
const pc = core.checkProject(cfg, 200, project);
ok(pc.defaultBranch === 'release/main' && pc.defaultBranchPath === 'release%2Fmain', 'default branch is path-escaped');
ok(core.checkProject(cfg, 200, JSON.stringify(project)).defaultBranch === 'release/main', 'project body may arrive as a JSON string');
for (const [name, patch, status] of [['HTTP 404', {}, 404], ['internal', { visibility: 'internal' }], ['archived', { archived: true }],
  ['blank branch', { default_branch: ' ' }], ['string id', { id: '42' }], ['other id', { id: 7 }], ['other host', { web_url: 'https://evil.example/tools/p' }],
  ['outside path', { web_url: 'https://gitlab.example.com/p' }]]) {
  assert.throws(() => core.checkProject(cfg, status || 200, Object.assign({}, project, patch)), new RegExp(''), name);
  n++;
}
assert.throws(() => core.checkDefaultBranch('main', 200, { name: 'main', protected: false }));
assert.throws(() => core.checkDefaultBranch('main', 200, { name: 'other', protected: true }));
assert.throws(() => core.checkDefaultBranch('main', 403, { name: 'main', protected: true }));
core.checkDefaultBranch('main', 200, { name: 'main', protected: true });
n += 4;
ok(core.checkMergeRequest(cfg, 201, { iid: 3, web_url: 'https://gitlab.example.com/tools/p/-/merge_requests/3' }).iid === 3, 'MR accepted');
assert.throws(() => core.checkMergeRequest(cfg, 201, { iid: 3 }));
assert.throws(() => core.checkMergeRequest(cfg, 500, { web_url: 'https://gitlab.example.com/tools/p/-/merge_requests/3' }));
n += 2;
for (const s of [199, 300, 302, 0, 'x']) { assert.throws(() => core.gitlabStatus(s, 'x')); n++; }
core.gitlabStatus(201, 'x');

// Request identity and time in Go's formats
ok(/^[a-f0-9]{32}$/.test(core.randomHexId('x')) && core.randomHexId('x') !== core.randomHexId('x'), 'random 32-hex request ID');
ok(core.rfc3339Nano(new Date(Date.UTC(2026, 9, 29, 6, 0, 0, 120))) === '2026-10-29T06:00:00.12Z', 'RFC3339Nano trims fraction zeros');
ok(core.rfc3339Nano(new Date(Date.UTC(2026, 9, 29, 6, 0, 0, 0))) === '2026-10-29T06:00:00Z', 'RFC3339Nano drops a zero fraction');
ok(core.truncateUtf8('abc', 180) === 'abc' && core.truncateUtf8('ab✓', 4) === 'ab\ufffd\ufffd' && core.truncateUtf8('ab✓', 5) === 'ab✓', 'byte truncation like Go');

// promotion-variable check
ok(JSON.stringify(core.sensitivePaths({ b: '[REDACTED]', a: { s: [{ v: '[REDACTED]' }, { v: 'x' }] }, c: 'ok' }, '')) === '["a.s[0].v","b"]', 'sensitive paths, sorted, with array indexes');
ok(core.variableName('${promotionVariable: appSecret}') === 'appSecret' && core.variableName('${promotionVariable:x}') === 'x' && core.variableName('plain') === 'plain', 'variable reference parsing');
const vreq = { selected: ['r1', 'r2'], configurations: { r1: { secret: '[REDACTED]', name: 'n' }, r2: { name: 'n' } } };
let vr = core.variablesReport(vreq, [{ resourceId: 'r1', status: 404 }, { resourceId: 'r2', status: 404 }], [], '', [], '');
ok(vr.findings.length === 1 && vr.findings[0].state === 'missing' && vr.open === 1, 'undeclared secret is missing');
vr = core.variablesReport(vreq, [{ resourceId: 'r1', status: 200, body: JSON.stringify({ variables: { secret: '${promotionVariable: s}' } }) }], [],
  '', [{ _embedded: { referencedResources: [{ name: 's', value: '******' }] } }], '');
ok(vr.findings[0].state === 'ready' && vr.open === 0, 'masked definition counts as sensitive; body may be a JSON string');
vr = core.variablesReport(vreq, [{ resourceId: 'r1', status: 200, body: { variables: { secretX: 's' } } }], [], '', [], '');
ok(vr.findings[0].state === 'missing', 'a property that only shares a prefix does not cover the path');
vr = core.variablesReport(vreq, [{ resourceId: 'r1', status: 500 }], [], '', [], '');
ok(vr.findings[0].state === 'unknown' && /could not be read/.test(vr.reportText), 'server error is reported as unknown');
vr = core.variablesReport(vreq, [{ resourceId: 'r1', status: 200, body: { variables: { secret: 's' } } }], [], 'HTTP 403', [], '');
ok(vr.findings[0].state === 'unknown', 'unreadable definitions never claim a variable is undefined');
const many = { selected: ['r1'], configurations: { r1: { list: Array.from({ length: 250 }, () => '[REDACTED]') } } };
vr = core.variablesReport(many, [{ resourceId: 'r1', status: 404 }], [], '', [], '');
ok(vr.findings.length === 250 && (vr.mrText.match(/\n- /g) || []).length === 200 && /first 200 of 250/.test(vr.reportText), 'listing capped at 200');
ok(core.variablesReport({ selected: ['r1'], configurations: { r1: { name: 'x`y' } } }, [], [], '', [], '').mrText.indexOf('No sensitive values') > 0, 'no sensitive values');

// pages
ok(core.pagesComplete([{ count: 2, _embedded: { referencedResources: [1] } }, { _embedded: { referencedResources: [2] } }]), 'complete pages');
ok(!core.pagesComplete([{ count: 3, _embedded: { referencedResources: [1] } }]), 'incomplete pages');
ok(core.nextPageURL({ _links: { next: { href: '?page=2' } } }) === '?page=2' && core.nextPageURL({}) === '', 'next link');

ok(core.escapeHtml('<a href="x">&\'</a>') === '&lt;a href=&quot;x&quot;&gt;&amp;&#39;&lt;/a&gt;', 'escapeHtml');
console.log('unit: ' + n + ' checks passed');
