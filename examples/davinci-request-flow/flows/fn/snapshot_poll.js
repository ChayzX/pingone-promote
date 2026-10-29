// After POST /snapshots or GET /snapshots/<env>/versions/<id>: decide whether
// the snapshot is COMPLETE, still preparing (loop), or failed. Mirrors
// pingone.Inventory: up to 90 polls, ERROR fails, identity must match.
const env = String(params.environmentId || '');
const attempts = Number(params.attempts || 0);
const body = core.parseBody(params.body) || {};
const status = Number(params.statusCode);
// record: the snapshot identity and status only (an expanded snapshot body can be large).
const record = { snapshotId: body.snapshotId, status: body.status, resource: body.resource && typeof body.resource === 'object' ? { id: body.resource.id } : body.resource };
const out = (state, error) => ({ state: state, error: error || '', snapshotId: String(body.snapshotId || ''), attempts: attempts + 1, record: record,
  versionUrl: core.SAFE_ID.test(String(body.snapshotId || '')) ? params.apiOrigin + '/v1/environments/' + env + '/snapshots/' + env + '/versions/' + body.snapshotId : '' });
if (status < 200 || status > 299) { return out('error', 'Snapshot request failed (HTTP ' + status + ').'); }
if (!core.SAFE_ID.test(String(body.snapshotId || '')) || !body.resource || body.resource.id !== env) {
  return out('error', 'Snapshot response omitted its environment or version identity.');
}
if (body.status === 'COMPLETE') { return out('complete'); }
if (body.status === 'ERROR') { return out('error', 'PingOne snapshot failed; inspect snapshot errors in PingOne.'); }
if (attempts + 1 >= 90) { return out('error', 'Snapshot is still preparing; try again later.'); }
return out('wait');
