// Stage 4 (end): subflow tiers, variables and connections, then the
// deterministic tfvars text for environments/<target>.tfvars.
const s = lib.state(params.state);
const frags = lib.obj(params.frags);
const e = lib.enter(s);
lib.begin(s, 'translate', e.t0);
let text = '';
try {
  const r = tr.assemble(frags, s.maps || {}, s.target);
  text = r.text;
  const ph = r.stats.placeholders;
  s.tfvars = { bytes: lib.utf8Len(text), lines: text.split('\n').length - 1, flows: r.flows, connections: r.connections, variables: r.variables,
    tiers: r.tiers, placeholders: { connection: ph.connection || 0, flow: ph.flow || 0, variable: ph.variable || 0, secret: ph.secret || 0, env: ph.env || 0 },
    secretsMasked: r.stats.secretsMasked, unresolvedIds: r.stats.unresolvedIds, droppedSettings: r.stats.droppedSettings,
    externalSubflows: r.externalSubflows.length, problems: r.problems.length };
  // Key names only (they are Terraform variable names), never values.
  s.secretKeys = r.secrets.slice(0, 500);
  for (const p of r.problems.slice(0, 10)) { lib.note(s, 'Translation: ' + lib.redact(p)); }
  if (r.externalSubflows.length) { lib.note(s, r.externalSubflows.length + ' subflow(s) called by fetched flows are not in the snippet; fetch more flows to place them in tiers.'); }
  s.stages.translate.items = r.flows;
} catch (err) {
  lib.fail(s, 'translate', { where: 'assemble tfvars', status: 0, code: 'TRANSLATE', message: lib.redact(err.message || err) });
}
const ms = lib.now() - e.t0;
s.stages.translate.assembleMs = ms;
s.stages.translate.bytesOut = lib.utf8Len(text);
lib.end(s, 'translate', s.stages.translate.errors ? 'error' : 'ok');
let action = 'finish';
if (s.gitlab && text) {
  lib.begin(s, 'gitlab');
  s.gl = { path: 'environments/' + s.target + '.tfvars' };
  s.url = lib.gitlabUrl(s, '');
  action = 'gitlab';
} else {
  lib.end(s, 'gitlab', 'skipped');
}
lib.leave(s, 'translate', e.t0);
lib.track(s);
return lib.out(s, action, 'Translated', { tfvars: text });
