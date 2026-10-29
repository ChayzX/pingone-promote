// Fixtures for the intent cross-check: intents as the flow builds them, and
// variants that break each rule of gitops.ValidateIntent. Prints JSON.
const core = require('./load_core');
const base = () => ({ version: 1, id: 'abcdef0123456789abcdef0123456789', requestedBy: { pingoneUserId: 'sign-in-off', username: 'Sign-in off (proof of concept)' },
  source: { id: '11111111-1111-4111-8111-111111111111', region: 'us' }, target: { id: '22222222-2222-4222-8222-222222222222', region: 'us' },
  selected: ['f1', 'a1'], mappings: { f1: 'tf1' }, description: 'Fix <login> & "MFA"\nsecond line' });
const fx = [];
const add = (name, mutate) => { const i = base(); mutate(i); fx.push({ name, json: JSON.stringify(i, null, 2) + '\n', obj: i }); };
add('as built', () => {});
add('unicode description', (i) => { i.description = 'Ünïcödé 日本語 ✓'; });
add('tab allowed', (i) => { i.description = 'a\tb'; });
add('carriage return refused', (i) => { i.description = 'a\r\nb'; });
add('C1 control refused', (i) => { i.description = 'a\u0085b'; });
add('description 3000 bytes', (i) => { i.description = 'x'.repeat(3000); });
add('description 3001 bytes', (i) => { i.description = 'x'.repeat(3001); });
add('description multi-byte over 3000', (i) => { i.description = 'é'.repeat(1501); });
add('username 256 bytes', (i) => { i.requestedBy.username = 'u'.repeat(256); });
add('username 257 bytes', (i) => { i.requestedBy.username = 'u'.repeat(257); });
add('empty username', (i) => { i.requestedBy.username = ''; });
add('bad user id', (i) => { i.requestedBy.pingoneUserId = 'a b'; });
add('version 2', (i) => { i.version = 2; });
add('uppercase id', (i) => { i.id = 'ABCDEF0123456789ABCDEF0123456789'; });
add('short id', (i) => { i.id = 'abc'; });
add('same environments', (i) => { i.target.id = i.source.id; });
add('bad region', (i) => { i.source.region = 'mars'; });
add('every region', (i) => { i.source.region = 'asia'; i.target.region = 'au'; });
add('unsafe environment id', (i) => { i.target.id = '../x'; });
add('nothing selected', (i) => { i.selected = []; });
add('duplicate selection', (i) => { i.selected = ['f1', 'f1']; });
add('unsafe selection', (i) => { i.selected = ['f1', 'a/b']; });
add('10000 selected', (i) => { i.selected = Array.from({ length: 10000 }, (_, k) => 'r' + k); });
add('10001 selected', (i) => { i.selected = Array.from({ length: 10001 }, (_, k) => 'r' + k); });
add('two sources map to one target', (i) => { i.mappings = { f1: 't', a1: 't' }; });
add('mapping of an unselected resource', (i) => { i.mappings = { zz: 'tz' }; });
add('unsafe mapping', (i) => { i.mappings = { f1: 't t' }; });
add('no mappings', (i) => { i.mappings = {}; });
add('empty description', (i) => { i.description = ''; });
// The builder's own output for random selections.
for (let k = 0; k < 30; k++) {
  const sel = Array.from({ length: 1 + (k % 7) }, (_, j) => 'res' + k + '_' + j);
  const maps = {}; sel.forEach((s, j) => { if (j % 2 === 0) { maps[s] = 't' + s; } });
  const built = core.newIntent(core.randomHexId('x' + k), { userId: 'u' + k, username: 'User ' + k }, { id: 'env-a' + k, region: 'eu' },
    { id: 'env-b' + k, region: 'eu' }, sel, maps, 'Change ' + k + '\nwith detail');
  fx.push({ name: 'built ' + k, json: built.json, obj: built.intent });
}
console.log(JSON.stringify(fx));
