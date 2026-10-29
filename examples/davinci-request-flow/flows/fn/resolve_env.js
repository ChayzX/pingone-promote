// Turn an environment button (ENV1..ENV4) into that environment's configured
// ID and name. Refuses an empty slot, an unsafe ID, and the excluded ID (the
// source, when choosing the target).
const choice = String(params.choice || '');
const m = /^ENV([1-4])$/.exec(choice);
const fail = (msg) => ({ state: 'error', error: core.escapeHtml(msg), environmentId: '', name: '', nameText: '' });
if (!m) { return fail('Choose one of the listed environments.'); }
const slots = {
  1: [params.env1Id, params.env1Name], 2: [params.env2Id, params.env2Name],
  3: [params.env3Id, params.env3Name], 4: [params.env4Id, params.env4Name]
};
const id = String(slots[m[1]][0] || '').trim();
const name = String(slots[m[1]][1] || '').trim().slice(0, 100);
if (!id || !name) { return fail('That environment slot is not configured. Set env' + m[1] + 'Name and env' + m[1] + 'Id in the flow variables.'); }
if (!core.SAFE_ID.test(id)) { return fail('env' + m[1] + 'Id is not a valid environment ID.'); }
if (id === String(params.excludeId || '')) { return fail('Source and target must be different environments.'); }
return { state: 'ok', error: '', environmentId: id, name: name, nameText: core.escapeHtml(name) };
