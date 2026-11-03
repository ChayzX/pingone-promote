// Stage 3 (and per-flow stage 4): one full flow by ID, translated at once so
// only its fragment, not the raw flow, is carried to the next iteration.
const s = lib.state(params.state);
const frags = lib.obj(params.frags);
if (!frags.flows) { frags.flows = {}; }
const e = lib.enter(s);
const body = lib.parse(params.body);
const bytes = lib.size(body);
lib.call(s, 'fetch', e.since, params.statusCode, bytes, 0);
let abort = false;
let trMs = 0;
if (lib.ok2xx(params.statusCode) && body && typeof body === 'object' && typeof body.id === 'string') {
  const t1 = lib.now();
  lib.begin(s, 'translate', t1);
  s.stages.translate.interleaved = true;
  try {
    const frag = tr.translateFlow(body, s.maps, s.cfg.sourceEnvironmentId);
    frags.flows[frag.key] = frag;
    s.stages.translate.items += 1;
    s.stages.translate.bytesIn += bytes;
  } catch (err) {
    lib.fail(s, 'translate', { where: 'translate flow', status: 0, code: 'TRANSLATE', message: lib.redact(err.message || err) });
  }
  trMs = lib.now() - t1;
  s.stages.translate.fnMs += trMs;
  s.stages.translate.maxFnMs = Math.max(s.stages.translate.maxFnMs, trMs);
  s.fetch.ok += 1;
  s.stages.fetch.items += 1;
  s.fetch.maxFlowBytes = Math.max(s.fetch.maxFlowBytes || 0, bytes);
} else {
  s.fetch.failed += 1;
  lib.fail(s, 'fetch', lib.errInfo('get flow ' + (s.fetch.i + 1), params.statusCode, body));
  // A rejected token or role ends the run; other failures are counted and skipped.
  const st = Number(params.statusCode);
  abort = st === 401 || st === 403 || s.fetch.failed > 5;
}
s.fetch.i += 1;
const total = s.fetch.ids.length;
let action;
if (abort) {
  lib.end(s, 'fetch', 'error', e.t0);
  action = 'finish';
} else if (s.fetch.i < total) {
  s.url = lib.flowUrl(s, s.fetch.ids[s.fetch.i]);
  action = lib.shouldPause(s) ? 'pause' : 'more';
} else {
  lib.end(s, 'fetch', s.fetch.failed ? 'warn' : 'ok', e.t0);
  action = 'assemble';
}
lib.leave(s, 'fetch', e.t0);
// The translation time was charged to stage 4, not to the fetch stage.
s.stages.fetch.fnMs = Math.max(0, s.stages.fetch.fnMs - trMs);
lib.track(s, frags);
return lib.out(s, action, 'Fetched ' + s.fetch.i + ' of ' + total + ' flows', { frags: frags });
