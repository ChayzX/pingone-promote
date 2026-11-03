// A working page came back from the browser: count the round trip, start a
// new segment and continue the phase that paused.
const s = lib.state(params.state);
const e = lib.enter(s);
s.totals.handbacks += 1;
s.totals.handbackMs += e.since;
lib.segment(s, e.t0);
const action = s.phase === 'fetch' ? 'fetch' : (s.phase === 'inventory' ? 'inventory' : 'finish');
lib.leave(s, '', e.t0);
lib.track(s);
return lib.out(s, action, 'Continuing');
