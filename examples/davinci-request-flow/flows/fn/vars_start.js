// Start the promotion-variable check: the selected source resource IDs, in
// request order, to read one declaration each.
try {
  const request = JSON.parse(String(params.requestJson || ''));
  const ids = request.selected.filter((id) => core.SAFE_ID.test(id));
  if (ids.length === 0 || ids.length !== request.selected.length) { throw new Error('no valid selection'); }
  return { state: 'ok', error: '', ids: ids, firstId: ids[0] };
} catch (e) {
  return { state: 'error', error: core.escapeHtml(String(e.message || e)), ids: [], firstId: '' };
}
