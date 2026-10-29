// Build source and target inventories from their environment records,
// COMPLETE snapshot records and collected pages, and list promotable
// resources for the selection page. pinned=true uses InventoryVersion rules.
try {
  const pinned = String(params.pinned) === 'true';
  const src = core.inventoryFromSnapshots(params.apiOrigin, String(params.sourceEnvironmentId), core.parseBody(params.sourceEnvBody),
    core.parseBody(params.sourceSnapshotBody), core.parseBody(params.sourcePages) || [], pinned, String(params.sourceSnapshotId || ''));
  const tgt = core.inventoryFromSnapshots(params.apiOrigin, String(params.targetEnvironmentId), core.parseBody(params.targetEnvBody),
    core.parseBody(params.targetSnapshotBody), core.parseBody(params.targetPages) || [], pinned, String(params.targetSnapshotId || ''));
  const line = (r) => r.type + '  ' + r.id + '  ' + r.name;
  const listText = core.escapeHtml(src.resources.map(line).sort().join('\n') || 'No resources found.');
  const targetText = core.escapeHtml(tgt.resources.map(line).sort().join('\n') || 'No resources found.');
  return { state: 'ok', error: '', source: src, target: tgt, sourceListText: listText, targetListText: targetText };
} catch (e) {
  return { state: 'error', error: core.escapeHtml(String(e.message || e)), source: null, target: null, sourceListText: '', targetListText: '' };
}
