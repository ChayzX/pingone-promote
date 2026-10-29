// Build source and target inventories from their environment records,
// COMPLETE snapshot records and collected pages, and list promotable
// resources for the selection page. pinned=true uses InventoryVersion rules.
// Like the local app (/api/mappings): suggest destination mappings from the
// saved PingOne promotion mappings, then by unique same-type name; a failed
// read of the saved mappings is a warning, not an error.
try {
  const pinned = String(params.pinned) === 'true';
  const src = core.inventoryFromSnapshots(params.apiOrigin, String(params.sourceEnvironmentId), core.parseBody(params.sourceEnvBody),
    core.parseBody(params.sourceSnapshotBody), core.parseBody(params.sourcePages) || [], pinned, String(params.sourceSnapshotId || ''));
  const tgt = core.inventoryFromSnapshots(params.apiOrigin, String(params.targetEnvironmentId), core.parseBody(params.targetEnvBody),
    core.parseBody(params.targetSnapshotBody), core.parseBody(params.targetPages) || [], pinned, String(params.targetSnapshotId || ''));
  const line = (r) => r.type + '  ' + r.id + '  ' + r.name;
  const listText = core.escapeHtml(src.resources.map(line).sort().join('\n') || 'No resources found.');
  const targetText = core.escapeHtml(tgt.resources.map(line).sort().join('\n') || 'No resources found.');
  const warnings = [];
  const status = Number(params.mappingsStatus);
  const saved = core.parseBody(params.mappingsBody);
  let native = {};
  if (status >= 200 && status <= 299 && saved && typeof saved === 'object') {
    native = saved.resourceMappings && typeof saved.resourceMappings === 'object' ? saved.resourceMappings : {};
  } else {
    warnings.push('Saved PingOne mappings could not be read. Review suggestions and the final native plan: HTTP ' + (isFinite(status) && status ? status : 'error') + '.');
  }
  const suggestions = core.suggestMappings(src, tgt, native);
  // What the picker needs, never configuration. HTML-escaped: the page puts it inside a <textarea>.
  const picker = {
    source: src.resources.map((r) => ({ id: r.id, name: r.name, type: r.type, dependencies: r.dependencies || [] })),
    target: tgt.resources.map((r) => ({ id: r.id, name: r.name, type: r.type })),
    suggestions: suggestions, warnings: warnings
  };
  return { state: 'ok', error: '', source: src, target: tgt, sourceListText: listText, targetListText: targetText,
    pickerDataText: core.escapeHtml(JSON.stringify(picker)) };
} catch (e) {
  return { state: 'error', error: core.escapeHtml(String(e.message || e)), source: null, target: null, sourceListText: '', targetListText: '',
    pickerDataText: '' };
}
