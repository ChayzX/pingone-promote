// Compare the sensitive values in the request with the promotion variables
// PingOne already has. Advisory: the GitLab plan still enforces PingOne's rules.
try {
  const request = JSON.parse(String(params.requestJson || ''));
  const r = core.variablesReport(request, core.parseBody(params.declarations), core.parseBody(params.sourceDefinitionPages),
    String(params.sourceDefinitionError || ''), core.parseBody(params.targetDefinitionPages), String(params.targetDefinitionError || ''));
  return { state: 'ok', error: '', open: r.open, count: r.findings.length, summaryText: core.escapeHtml(r.summaryText),
    reportText: core.escapeHtml(r.reportText), variablesMd: r.mrText };
} catch (e) {
  const msg = 'Promotion variables could not be checked: ' + String(e.message || e);
  return { state: 'ok', error: '', open: 0, count: 0, summaryText: core.escapeHtml(msg), reportText: core.escapeHtml(msg),
    variablesMd: '\n\n### Promotion variables\n\n' + msg.replace(/[`\r\n]/g, '') };
}
