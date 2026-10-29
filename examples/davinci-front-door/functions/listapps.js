module.exports = a = async ({params}) => {
  // Turn the source environment's application list into escaped plain-text
  // lines (name, then the ID to paste) for a <pre> block on the selection form.
  const esc = (s) => String(s).replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
  let body = params.applicationsBody;
  if (typeof body === 'string') { try { body = JSON.parse(body); } catch (e) { body = {}; } }
  if (Number(params.applicationsStatus) !== 200 || !body) {
    return { listText: 'Could not list applications; enter resource IDs manually.', count: 0 };
  }
  const apps = ((body._embedded || {}).applications) || [];
  const rows = apps
    .filter((app) => app && app.id)
    .sort((x, y) => String(x.name || '').localeCompare(String(y.name || '')))
    .map((app) => esc(app.id) + '  ' + esc(app.name || '(unnamed)'));
  return { listText: rows.length ? rows.join('\n') : 'No applications found.', count: rows.length };
};
