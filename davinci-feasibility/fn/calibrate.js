// Calibration loop: function -> save -> evaluator, no HTTP. The time between
// two iterations is DaVinci's own per-node overhead (2 connector nodes, 3 evaluators).
const s = lib.state(params.state);
const e = lib.enter(s);
lib.begin(s, 'calibrate', e.t0);
if (!s.calib) {
  s.calib = { n: s.settings.calibrationLoops, i: 0, sumMs: 0, maxMs: 0 };
} else {
  s.calib.sumMs += e.since;
  s.calib.maxMs = Math.max(s.calib.maxMs, e.since);
}
let action = 'more';
if (s.calib.i < s.calib.n) {
  s.calib.i += 1;
  lib.stage(s, 'calibrate').items = s.calib.i;
} else {
  action = 'token';
  lib.end(s, 'calibrate', 'ok', e.t0);
  lib.begin(s, 'auth', e.t0);
}
lib.leave(s, action === 'token' ? 'auth' : 'calibrate', e.t0);
lib.track(s);
return { state: s, action: action, url: s.url, progress: 'Calibrating' };
