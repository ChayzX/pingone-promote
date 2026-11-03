// Translator embedded (as `tr`) in the fetch and assemble functions.
// Follows the hosted design's tfvars schema v1 conventions (ADR 0003, 0005,
// 0009 on the terraform/phase1-foundation branch) closely enough to measure
// the cost; it is not the hosted translator.
const tr = {};

// ---- logical keys (same encoding as internal/keys) ------------------------
tr.HEX = '0123456789ABCDEF';
tr.utf8Bytes = (s) => {
  const out = [];
  for (let i = 0; i < s.length; i++) {
    let c = s.charCodeAt(i);
    if (c >= 0xd800 && c <= 0xdbff && i + 1 < s.length) {
      c = 0x10000 + ((c - 0xd800) << 10) + (s.charCodeAt(i + 1) - 0xdc00);
      i++;
    }
    if (c < 0x80) { out.push(c); } else if (c < 0x800) { out.push(0xc0 | (c >> 6), 0x80 | (c & 63)); } else if (c < 0x10000) { out.push(0xe0 | (c >> 12), 0x80 | ((c >> 6) & 63), 0x80 | (c & 63)); } else { out.push(0xf0 | (c >> 18), 0x80 | ((c >> 12) & 63), 0x80 | ((c >> 6) & 63), 0x80 | (c & 63)); }
  }
  return out;
};
tr.encode = (name, escapeUnderscore) => {
  let k = '';
  for (const b of tr.utf8Bytes(String(name))) {
    const ch = String.fromCharCode(b);
    if ((b >= 65 && b <= 90) || (b >= 97 && b <= 122) || (b >= 48 && b <= 57) || (b === 95 && !escapeUnderscore)) { k += ch; } else if (b === 45) { k += '--'; } else { k += '-' + tr.HEX[b >> 4] + tr.HEX[b & 15]; }
  }
  return k;
};
tr.key = (name) => {
  if (!name) { throw new Error('resource name is empty'); }
  const k = tr.encode(name, false);
  if (k.length > 128) { throw new Error('logical key would exceed 128 characters'); }
  return k;
};
tr.CONTEXTS = ['company', 'flow', 'flowInstance', 'user'];
// Variable keys ALWAYS carry the context (and the flow for flow variables), so
// a company variable and a flow variable with one name never collide.
tr.variableKey = (name, context, flowKey) => {
  if (!name) { throw new Error('variable name is empty'); }
  if (tr.CONTEXTS.indexOf(context) < 0) { throw new Error('unknown variable context'); }
  let k = tr.encode(name, true) + '_' + context;
  if (context === 'flow') {
    if (!flowKey) { throw new Error('flow variable without its flow'); }
    k += '_' + flowKey;
  }
  if (k.length > 128) { throw new Error('logical key would exceed 128 characters'); }
  return k;
};
// Secret placeholder keys are Terraform variable names (customer_secrets.yml
// declares each as TF_VAR_<key>): ^[A-Za-z][A-Za-z0-9_]*$.
tr.tfName = (s) => String(s).replace(/[^A-Za-z0-9_]/g, '_');
tr.secretKey = (kind, owner, field) => 'davinci_' + kind + '_' + tr.tfName(owner) + (field ? '_' + tr.tfName(field) : '');
tr.variableSecretKey = (varKey) => 'davinci_variable_' + tr.tfName(varKey) + '_value';

// ---- scrubbing: IDs to placeholders, secrets out --------------------------
tr.ID_RE = /[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}|\b[0-9a-fA-F]{32}\b/g;
tr.SECRET_NAME = /(secret|password|passwd|apikey|api_key|privatekey|private_key|token)$/i;
tr.MASK = '******';
tr.isExpr = (v) => /^\s*(\{\{[\s\S]*\}\}|\$\{[\s\S]*\})\s*$/.test(v);
// ctx: { ids: {rawId: placeholder}, secretKind, secretOwner, secrets: {key: true}, stats }
tr.scrubString = (s, ctx) => {
  const esc = s.split('@@').join('@@@@');
  return esc.replace(tr.ID_RE, (m) => {
    const p = ctx.ids[m] || ctx.ids[m.toLowerCase()];
    if (p) {
      const kind = p.slice(2, p.indexOf(':'));
      ctx.stats.placeholders[kind] = (ctx.stats.placeholders[kind] || 0) + 1;
      return p;
    }
    ctx.stats.unresolvedIds += 1;
    return m;
  });
};
tr.secretPlaceholder = (ctx, field) => {
  const k = tr.secretKey(ctx.secretKind, ctx.secretOwner, field);
  ctx.secrets[k] = true;
  ctx.stats.secretsMasked += 1;
  ctx.stats.placeholders.secret = (ctx.stats.placeholders.secret || 0) + 1;
  return '@@secret:' + k + '@@';
};
// Walk a properties object. A property {value: ...} is secret when the API
// masked it, when it is marked secure, or when its name looks secret and the
// value is a literal (not a {{...}} expression).
tr.scrub = (v, ctx, propName) => {
  if (typeof v === 'string') {
    if (v === tr.MASK) { return tr.secretPlaceholder(ctx, propName || 'value'); }
    return tr.scrubString(v, ctx);
  }
  if (Array.isArray(v)) { return v.map((x) => tr.scrub(x, ctx, propName)); }
  if (v && typeof v === 'object') {
    const out = {};
    for (const k of Object.keys(v)) {
      const x = v[k];
      if (x && typeof x === 'object' && !Array.isArray(x) && typeof x.value === 'string' && x.value !== '' &&
          (x.secure === true || x.value === tr.MASK || (tr.SECRET_NAME.test(k) && !tr.isExpr(x.value)))) {
        const rest = {};
        for (const rk of Object.keys(x)) { if (rk !== 'value') { rest[rk] = x[rk]; } }
        const copy = tr.scrub(rest, ctx, k);
        copy.value = tr.secretPlaceholder(ctx, k);
        out[k] = copy;
      } else {
        out[k] = tr.scrub(x, ctx, k);
      }
    }
    return out;
  }
  return v;
};
tr.newStats = () => ({ unresolvedIds: 0, secretsMasked: 0, placeholders: {}, droppedSettings: 0 });
tr.mergeStats = (a, b) => {
  a.unresolvedIds += b.unresolvedIds;
  a.secretsMasked += b.secretsMasked;
  a.droppedSettings += b.droppedSettings || 0;
  for (const k of Object.keys(b.placeholders)) { a.placeholders[k] = (a.placeholders[k] || 0) + b.placeholders[k]; }
  return a;
};

// The ID map used for placeholders, built from the inventory maps.
tr.idMap = (maps, sourceEnvIdArg) => {
  const ids = {};
  const sourceEnvId = sourceEnvIdArg || maps.env || '';
  for (const id of Object.keys(maps.flows || {})) { ids[id] = '@@flow:' + maps.flows[id] + '@@'; }
  for (const id of Object.keys(maps.conns || {})) { ids[id] = '@@connection:' + maps.conns[id] + '@@'; }
  for (const id of Object.keys(maps.vars || {})) { ids[id] = '@@variable:' + maps.vars[id] + '@@'; }
  if (sourceEnvId) { ids[sourceEnvId] = '@@env:id@@'; ids[String(sourceEnvId).toLowerCase()] = '@@env:id@@'; }
  return ids;
};

// ---- one flow -> fragment ---------------------------------------------------
tr.SETTINGS = { csp: 'csp', css: 'css', cssLinks: 'css_links', customErrorScreenBrandLogoUrl: 'custom_error_screen_brand_logo_url',
  customErrorShowFooter: 'custom_error_show_footer', customFaviconLink: 'custom_favicon_link', customLogoURLSelection: 'custom_logo_urlselection',
  customTimeoutErrorScreenCSS: 'custom_timeout_error_screen_css', customTimeoutErrorScreenHTML: 'custom_timeout_error_screen_html',
  customTimeoutErrorScreenMessage: 'custom_timeout_error_screen_message', customTitle: 'custom_title', defaultErrorScreenBrandLogo: 'default_error_screen_brand_logo',
  flowHttpTimeoutInSeconds: 'flow_http_timeout_in_seconds', flowTimeoutInSeconds: 'flow_timeout_in_seconds',
  intermediateLoadingScreenCSS: 'intermediate_loading_screen_css', intermediateLoadingScreenHTML: 'intermediate_loading_screen_html',
  jsCustomFlowPlayer: 'js_custom_flow_player', logLevel: 'log_level', previewFormRenderingUpdates: 'preview_form_rendering_updates',
  requireAuthenticationToInitiate: 'require_authentication_to_initiate', scrubSensitiveInfo: 'scrub_sensitive_info', sensitiveInfoFields: 'sensitive_info_fields',
  useCSP: 'use_csp', useCustomCSS: 'use_custom_css', useCustomFlowPlayer: 'use_custom_flow_player', useCustomScript: 'use_custom_script',
  useCustomTimeoutErrorScreen: 'use_custom_timeout_error_screen', useIntermediateLoadingScreen: 'use_intermediate_loading_screen', validateOnSave: 'validate_on_save' };
tr.SUBFLOW_CAPS = ['startUiSubFlow', 'startSubFlow'];

tr.translateFlow = (flow, maps, sourceEnvId) => {
  const f = flow && typeof flow === 'object' ? flow : {};
  const key = (maps.flows || {})[f.id] || tr.key(f.name);
  const stats = tr.newStats();
  const secrets = {};
  const ctx = { ids: tr.idMap(maps, sourceEnvId), secretKind: 'flow', secretOwner: key, secrets: secrets, stats: stats };
  const out = { key: key, name: String(f.name || ''), nodes: {}, edges: {}, calls: [], connections: [] };
  if (typeof f.description === 'string' && f.description) { out.description = tr.scrubString(f.description, ctx); }
  if (typeof f.color === 'string' && f.color) { out.color = f.color; }
  if (typeof f.enabled === 'boolean') { out.enabled = f.enabled; }
  if (f.settings && typeof f.settings === 'object') {
    const st = {};
    for (const k of Object.keys(f.settings)) {
      const v = f.settings[k];
      if (!tr.SETTINGS[k] || v === null || v === undefined || (typeof v === 'object' && !Array.isArray(v))) { stats.droppedSettings += 1; continue; }
      st[tr.SETTINGS[k]] = typeof v === 'string' ? tr.scrubString(v, ctx) : v;
    }
    if (Object.keys(st).length) { out.settings = st; }
  }
  if (f.trigger && typeof f.trigger === 'object' && typeof f.trigger.type === 'string') {
    out.trigger = { type: f.trigger.type };
    if (typeof f.trigger.subtype === 'string') { out.trigger.subtype = f.trigger.subtype; }
  }
  if (Array.isArray(f.inputSchema) && f.inputSchema.length) {
    out.inputSchema = f.inputSchema.map((i) => {
      const o = {};
      for (const [a, b] of [['description', 'description'], ['isExpanded', 'is_expanded'], ['preferredControlType', 'preferred_control_type'],
        ['preferredDataType', 'preferred_data_type'], ['propertyName', 'property_name'], ['required', 'required']]) {
        if (i && i[a] !== undefined && i[a] !== null && i[a] !== '') { o[b] = i[a]; }
      }
      return o;
    });
  }
  const o = f.outputSchema && f.outputSchema.output;
  if (o && typeof o === 'object' && typeof o.type === 'string') {
    out.outputSchema = { type: o.type };
    if (o.properties && typeof o.properties === 'object') { out.outputSchema.properties = tr.scrub(o.properties, ctx); }
    if (typeof o.additionalProperties === 'boolean') { out.outputSchema.additionalProperties = o.additionalProperties; }
  }
  const els = (f.graphData && f.graphData.elements) || {};
  const nodes = Array.isArray(els.nodes) ? els.nodes : [];
  const conns = {};
  for (const n of nodes) {
    const d = (n && n.data) || {};
    if (!d.id) { continue; }
    ctx.secretOwner = key + '_' + d.id;
    const node = { node_type: d.nodeType || 'CONNECTION' };
    if (d.connectionId) {
      const ck = (maps.conns || {})[d.connectionId];
      if (ck) { node.connection = ck; conns[ck] = true; } else { stats.unresolvedIds += 1; node.connection_unresolved = true; }
    }
    for (const [a, b] of [['connectorId', 'connector_id'], ['capabilityName', 'capability_name'], ['label', 'label'], ['status', 'status'], ['type', 'type']]) {
      if (typeof d[a] === 'string' && d[a]) { node[b] = d[a]; }
    }
    if (d.properties && typeof d.properties === 'object' && Object.keys(d.properties).length) {
      if (tr.SUBFLOW_CAPS.indexOf(d.capabilityName) >= 0) {
        const sf = d.properties.subFlowId && d.properties.subFlowId.value;
        const sid = sf && typeof sf === 'object' ? sf.value : sf;
        if (typeof sid === 'string' && sid) { out.calls.push((maps.flows || {})[sid] || ('?' + out.calls.length)); }
      }
      node.properties = tr.scrub(d.properties, ctx);
    }
    if (n.position && typeof n.position.x === 'number' && typeof n.position.y === 'number') { node.position = { x: n.position.x, y: n.position.y }; }
    out.nodes[d.id] = node;
  }
  for (const e of Array.isArray(els.edges) ? els.edges : []) {
    const d = (e && e.data) || {};
    if (!d.id || !d.source || !d.target) { continue; }
    const edge = { source: d.source, target: d.target };
    if (typeof d.multiValueSourceId === 'string' && d.multiValueSourceId) { edge.multi_value_source_id = d.multiValueSourceId; }
    out.edges[d.id] = edge;
  }
  out.connections = Object.keys(conns).sort();
  out.secrets = Object.keys(secrets).sort();
  out.stats = stats;
  return out;
};

// ---- subflow tiers (ADR 0005): top-level flows in t3, callees below ----------
tr.assignTiers = (frags) => {
  const keys = Object.keys(frags).sort();
  const callers = {};
  const problems = [];
  const external = {};
  for (const k of keys) { callers[k] = []; }
  for (const k of keys) {
    for (const c of frags[k].calls || []) {
      if (c.charAt(0) === '?') { external['(unknown flow ID)'] = true; continue; }
      if (!frags[c]) { external[c] = true; continue; }
      if (callers[c].indexOf(k) < 0) { callers[c].push(k); }
    }
  }
  const depth = {};
  const visiting = {};
  const walk = (k) => {
    if (depth[k] !== undefined) { return depth[k]; }
    if (visiting[k]) { problems.push('subflow cycle through ' + k); return 0; }
    visiting[k] = true;
    let d = 0;
    for (const p of callers[k]) { d = Math.max(d, walk(p) + 1); }
    visiting[k] = false;
    depth[k] = d;
    return d;
  };
  const tiers = {};
  for (const k of keys) {
    const d = walk(k);
    if (d > 3) { problems.push(k + ' is nested ' + (d + 1) + ' levels deep; only 4 tiers exist'); }
    tiers[k] = Math.max(0, 3 - d);
  }
  return { tiers: tiers, problems: problems, external: Object.keys(external).sort() };
};

// ---- variables and connections ------------------------------------------------
tr.variableEntry = (v, maps, stats, secrets) => {
  const context = String(v.context || '');
  const flowKey = context === 'flow' ? (maps.flows || {})[(v.flow && v.flow.id) || ''] : '';
  const key = tr.variableKey(String(v.name || ''), context, flowKey || '');
  const e = { name: String(v.name), context: context, data_type: String(v.dataType || 'string'), mutable: v.mutable !== false };
  if (typeof v.displayName === 'string' && v.displayName) { e.display_name = v.displayName; }
  if (context === 'flow') { e.flow = flowKey; }
  if (typeof v.min === 'number') { e.min = v.min; }
  if (typeof v.max === 'number') { e.max = v.max; }
  const masked = v.value === tr.MASK || (v.value && typeof v.value === 'object' && v.value.value === tr.MASK);
  if (e.data_type === 'secret' || masked) {
    // Never emitted: the pipeline supplies the value as TF_VAR_<key>.
    e.secret = tr.variableSecretKey(key);
    secrets[e.secret] = true;
    stats.secretsMasked += 1;
  } else if (v.value !== undefined && v.value !== null) {
    const raw = v.value && typeof v.value === 'object' && !Array.isArray(v.value) && 'value' in v.value && Object.keys(v.value).length === 1 ? v.value.value : v.value;
    const ctx = { ids: tr.idMap(maps, ''), secretKind: 'variable', secretOwner: key, secrets: secrets, stats: stats };
    if (typeof raw === 'string') { e.value = { string: tr.scrubString(raw, ctx) }; } else if (typeof raw === 'boolean') { e.value = { bool: raw }; } else if (typeof raw === 'number') { e.value = { float32: raw }; } else if (raw && typeof raw === 'object') { e.value = { json_object: tr.scrub(Array.isArray(raw) ? { items: raw } : raw, ctx) }; }
  }
  return { key: key, entry: e };
};
tr.connectionEntry = (c, key, maps, stats, secrets) => {
  const e = { name: String(c.name || ''), connector: String((c.connector && c.connector.id) || c.connectorId || '') };
  if (c.properties && typeof c.properties === 'object' && Object.keys(c.properties).length) {
    const ctx = { ids: tr.idMap(maps, ''), secretKind: 'connection', secretOwner: key, secrets: secrets, stats: stats };
    e.properties = tr.scrub(c.properties, ctx);
  }
  return e;
};

// ---- deterministic HCL-ish rendering ---------------------------------------------
// Not terraform fmt (that cannot run in DaVinci); it imitates its alignment.
// A function replacement: in a replacement string "$$" would mean a single "$".
tr.escTemplate = (s) => s.replace(/\$\{/g, () => '$${').replace(/%\{/g, () => '%%{');
tr.q = (s) => '"' + tr.escTemplate(String(s).replace(/\\/g, '\\\\').replace(/"/g, '\\"').replace(/\n/g, '\\n').replace(/\r/g, '\\r').replace(/\t/g, '\\t')
  .replace(/[\u0000-\u001f\u007f]/g, (c) => '\\u' + ('0000' + c.charCodeAt(0).toString(16).toUpperCase()).slice(-4))) + '"';
tr.canon = (v, d) => {
  const ind = (n) => '  '.repeat(n);
  d = d || 0;
  if (v === null || v === undefined) { return 'null'; }
  if (Array.isArray(v)) {
    if (!v.length) { return '[]'; }
    return '[\n' + v.map((x) => ind(d + 1) + tr.canon(x, d + 1)).join(',\n') + '\n' + ind(d) + ']';
  }
  if (typeof v === 'object') {
    const ks = Object.keys(v).sort();
    if (!ks.length) { return '{}'; }
    return '{\n' + ks.map((k) => ind(d + 1) + JSON.stringify(k) + ': ' + tr.canon(v[k], d + 1)).join(',\n') + '\n' + ind(d) + '}';
  }
  if (typeof v === 'number') { return isFinite(v) ? String(v) : 'null'; }
  return JSON.stringify(v);
};
// Body items: [name, value] where value is {raw}, {json}, {block: items}, {map: [[key, items]]}, {list: [items...]}
tr.raw = (s) => ({ raw: s });
tr.lit = (v) => {
  if (typeof v === 'string') { return { raw: tr.q(v) }; }
  if (typeof v === 'boolean' || typeof v === 'number') { return { raw: String(v) }; }
  if (Array.isArray(v) && v.every((x) => typeof x === 'string')) { return { raw: '[' + v.map(tr.q).join(', ') + ']' }; }
  if (Array.isArray(v)) { return { list: v.map((x) => tr.items(x)) }; }
  if (v && typeof v === 'object') { return { block: tr.items(v) }; }
  return { raw: 'null' };
};
tr.items = (o) => Object.keys(o).filter((k) => o[k] !== undefined).map((k) => [k, tr.lit(o[k])]);
tr.render = (items, depth, quoteKeys) => {
  const ind = '  '.repeat(depth);
  const lines = [];
  let i = 0;
  while (i < items.length) {
    // Align a run of single-line attributes; a heredoc ends the run.
    let j = i;
    const run = [];
    while (j < items.length && (items[j][1].raw !== undefined || items[j][1].json !== undefined)) {
      run.push(items[j]);
      j++;
      if (items[j - 1][1].json !== undefined) { break; }
    }
    if (run.length) {
      const name = (k) => (quoteKeys ? tr.q(k) : k);
      const w = Math.max.apply(null, run.map((r) => name(r[0]).length));
      for (const [k, v] of run) {
        const pad = name(k) + ' '.repeat(w - name(k).length);
        if (v.raw !== undefined) {
          lines.push(ind + pad + ' = ' + v.raw);
        } else {
          lines.push(ind + pad + ' = <<-JSON');
          for (const l of tr.canon(v.json).split('\n')) { lines.push(ind + '  ' + tr.escTemplate(l)); }
          lines.push(ind + 'JSON');
        }
      }
      i = j;
      continue;
    }
    const [k, v] = items[i];
    const name = quoteKeys ? tr.q(k) : k;
    if (v.block !== undefined) {
      lines.push(v.block.length ? ind + name + ' = {\n' + tr.render(v.block, depth + 1, false) + '\n' + ind + '}' : ind + name + ' = {}');
    } else if (v.map !== undefined) {
      if (!v.map.length) {
        lines.push(ind + name + ' = {}');
      } else {
        const inner = v.map.map(([mk, mv]) => (mv.raw !== undefined ? [mk, mv] : [mk, { block: mv }]));
        lines.push(ind + name + ' = {\n' + tr.render(inner, depth + 1, true) + '\n' + ind + '}');
      }
    } else if (v.list !== undefined) {
      const ind2 = '  '.repeat(depth + 1);
      lines.push(ind + name + ' = [\n' + v.list.map((it) => ind2 + '{\n' + tr.render(it, depth + 2, false) + '\n' + ind2 + '},').join('\n') + '\n' + ind + ']');
    }
    i++;
  }
  return lines.join('\n');
};
tr.nodeItems = (n) => {
  const it = [];
  for (const k of ['node_type', 'connection', 'connector_id', 'capability_name', 'label', 'status', 'type']) {
    if (n[k] !== undefined) { it.push([k, tr.lit(n[k])]); }
  }
  if (n.properties) { it.push(['properties', { json: n.properties }]); }
  if (n.position) { it.push(['position', { block: tr.items(n.position) }]); }
  return it;
};
tr.flowItems = (f, tier) => {
  const it = [['name', tr.lit(f.name)]];
  if (f.description !== undefined) { it.push(['description', tr.lit(f.description)]); }
  if (f.color !== undefined) { it.push(['color', tr.lit(f.color)]); }
  it.push(['tier', tr.raw(String(tier))]);
  if (f.enabled !== undefined) { it.push(['enabled', tr.lit(f.enabled)]); }
  if (f.settings) {
    const s = {};
    for (const k of Object.keys(f.settings).sort()) {
      const v = f.settings[k];
      s[k] = Array.isArray(v) ? v.map(String).filter((x, i, a) => a.indexOf(x) === i).sort() : v;
    }
    it.push(['settings', { block: tr.items(s) }]);
  }
  if (f.trigger) { it.push(['trigger', { block: tr.items(f.trigger) }]); }
  if (f.inputSchema) { it.push(['input_schema', { list: f.inputSchema.map((x) => tr.items(x)) }]); }
  if (f.outputSchema) {
    const o = [['type', tr.lit(f.outputSchema.type)]];
    if (f.outputSchema.properties) { o.push(['properties', { json: f.outputSchema.properties }]); }
    if (f.outputSchema.additionalProperties !== undefined) { o.push(['additional_properties', tr.lit(f.outputSchema.additionalProperties)]); }
    it.push(['output_schema', { block: [['output', { block: o }]] }]);
  }
  const nodes = Object.keys(f.nodes).sort().map((k) => [k, tr.nodeItems(f.nodes[k])]);
  const edges = Object.keys(f.edges).sort().map((k) => [k, tr.items(f.edges[k])]);
  it.push(['graph', { block: [['nodes', { map: nodes }], ['edges', { map: edges }]] }]);
  return it;
};
tr.variableItems = (e) => {
  const it = [];
  for (const k of ['name', 'context', 'data_type', 'mutable', 'display_name', 'flow', 'min', 'max', 'secret']) {
    if (e[k] !== undefined) { it.push([k, tr.lit(e[k])]); }
  }
  if (e.value) {
    const k = Object.keys(e.value)[0];
    it.push(['value', { block: [[k, k === 'json_object' ? { json: e.value[k] } : tr.lit(e.value[k])]] }]);
  }
  return it;
};

// frags: {flows: {key: fragment}, inv: {connections: [...], variables: [...]}}
tr.assemble = (frags, maps, target) => {
  const flows = (frags && frags.flows) || {};
  const inv = (frags && frags.inv) || {};
  const stats = tr.newStats();
  const secrets = {};
  const problems = [];
  const tiers = tr.assignTiers(flows);
  problems.push.apply(problems, tiers.problems);
  const usedConns = {};
  for (const k of Object.keys(flows)) {
    tr.mergeStats(stats, flows[k].stats || tr.newStats());
    for (const s of flows[k].secrets || []) { secrets[s] = true; }
    for (const c of flows[k].connections || []) { usedConns[c] = true; }
  }
  // Connections the snippet's flows use, keyed by name.
  const connById = {};
  for (const c of inv.connections || []) { connById[c.id] = c; }
  const connEntries = [];
  for (const id of Object.keys(maps.conns || {}).sort()) {
    const ck = maps.conns[id];
    if (!usedConns[ck] || !connById[id]) { continue; }
    const e = tr.connectionEntry(connById[id], ck, maps, stats, secrets);
    const it = [['name', tr.lit(e.name)], ['connector', tr.lit(e.connector)]];
    if (e.properties) { it.push(['properties', { json: e.properties }]); }
    connEntries.push([ck, it]);
  }
  // Company, flowInstance and user variables, plus flow variables of fetched flows.
  const varEntries = {};
  for (const v of inv.variables || []) {
    const ctx = String(v.context || '');
    if (ctx === 'flow') {
      const fk = (maps.flows || {})[(v.flow && v.flow.id) || ''];
      if (!fk || !flows[fk]) { continue; }
    } else if (tr.CONTEXTS.indexOf(ctx) < 0) {
      problems.push('variable with unknown context skipped');
      continue;
    }
    let r;
    try { r = tr.variableEntry(v, maps, stats, secrets); } catch (e) { problems.push('variable skipped: ' + e.message); continue; }
    if (varEntries[r.key]) { problems.push('two variables share the key ' + r.key); continue; }
    varEntries[r.key] = r.entry;
  }
  const flowEntries = Object.keys(flows).sort().map((k) => [k, tr.flowItems(flows[k], tiers.tiers[k])]);
  const top = [['tfvars_schema_version', tr.raw('1')], ['pingone_environment_id', tr.lit('')]];
  const blocks = [
    ['davinci_connections', { map: connEntries }],
    ['davinci_variables', { map: Object.keys(varEntries).sort().map((k) => [k, tr.variableItems(varEntries[k])]) }],
    ['davinci_flows', { map: flowEntries }]
  ];
  const text = '# Managed by pingone-promote (tfvars schema v1). Comments outside this header are not preserved.\n' +
    '# Feasibility snippet for environments/' + target + '.tfvars, rendered inside DaVinci: run terraform fmt in the pipeline.\n\n' +
    tr.render(top, 0, false) + '\n\n' + blocks.map((b) => tr.render([b], 0, false)).join('\n\n') + '\n';
  const tierCounts = { t0: 0, t1: 0, t2: 0, t3: 0 };
  for (const k of Object.keys(tiers.tiers)) { tierCounts['t' + tiers.tiers[k]] += 1; }
  return { text: text, secrets: Object.keys(secrets).sort(), problems: problems, externalSubflows: tiers.external, tiers: tierCounts,
    connections: connEntries.length, variables: Object.keys(varEntries).length, flows: flowEntries.length, stats: stats };
};
