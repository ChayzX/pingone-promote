// After GET /jobs/:id/trace with Range: bytes=-65536 (the last 64 KiB of the job log). Used only when
// result.json came back as a redirect or 404 for a finished job. Only the last PINGONE_PROMOTE_RESULT line is
// read (contract.resultFromLog); log text is never kept or shown.
const s = lib.state(params.state);
const status = Number(params.statusCode) || 0;
const run = s.run;
const now = lib.now();
s.notes = [];
s.q.todo = [];
s.q.calls += 1;
const found = lib.ok2xx(status) ? contract.resultFromLog(params.body, s.req.requestId) : { result: null, why: 'the job log could not be read (HTTP ' + status + ')' };
if (found.result) {
  lib.note(s, 'info', 'result.json could not be downloaded, so the app used the closing summary in the job log. The full change list and diff are not available here.');
  lib.finishResult(s, found.result, now);
} else {
  lib.finishNoResult(s, 'result.json could not be downloaded (' + contract.artifactMissing(Number(run.artifactStatus) || 0) + '), and ' + found.why + '. Open the job in GitLab to see what happened.', now);
}
return lib.out(s, 'render');
