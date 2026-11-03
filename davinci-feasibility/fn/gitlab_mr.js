// Stage 5f (optional): merge request result.
const s = lib.state(params.state);
const e = lib.enter(s);
const body = lib.parse(params.body);
lib.call(s, 'gitlab', e.since, params.statusCode, lib.size(body), 0);
if (lib.ok2xx(params.statusCode) && body && typeof body === 'object') {
  s.gl.mrOpened = true;
  // Shown on the result screen only, never in the copied results.
  const url = typeof body.web_url === 'string' ? body.web_url : '';
  const api = lib.splitUrl(s.cfg.gitlabApi);
  const u = lib.splitUrl(url);
  s.gl.mrUrl = u && api && u.origin === api.origin ? url : '';
  lib.end(s, 'gitlab', 'ok', e.t0);
} else {
  lib.fail(s, 'gitlab', lib.errInfo('merge request', params.statusCode, body));
  lib.end(s, 'gitlab', 'error', e.t0);
}
lib.leave(s, 'gitlab', e.t0);
return lib.out(s, 'finish', 'Done');
