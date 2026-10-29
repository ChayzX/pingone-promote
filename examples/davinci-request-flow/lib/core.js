// PingOne Promote core logic for DaVinci custom functions.
// A line-by-line port of what the Go tool's local app does before a request
// reaches GitLab:
//   internal/promotion/plan.go          -> redact, buildRequest
//   internal/pingone/inventory.go, pinned.go -> inventoryFromSnapshots
//   internal/gitops/request.go          -> newRequest, validateRequest, goJSON
//   internal/gitlab/client.go, config.go -> gitlabConfig, checkProject,
//                                          checkDefaultBranch, submitBodies
// Planning, approval, apply and rollback stay in GitLab (pingone-runner).
// Only ECMAScript built-ins are used (no URL, no crypto), because DaVinci
// custom functions guarantee nothing more. Everything is pure: API calls are
// made by HTTP nodes and their bodies are passed in.

const SAFE_ID = /^[A-Za-z0-9][A-Za-z0-9_-]{0,255}$/;

// ---------- JSON helpers --------------------------------------------------

function isObject(v) { return v !== null && typeof v === 'object' && !Array.isArray(v); }

// Canonical JSON: object keys sorted (as Go's encoding of map[string]any).
function canonical(v) {
  if (Array.isArray(v)) { return '[' + v.map(canonical).join(',') + ']'; }
  if (isObject(v)) {
    return '{' + Object.keys(v).sort().map((k) => JSON.stringify(k) + ':' + canonical(v[k])).join(',') + '}';
  }
  return JSON.stringify(v === undefined ? null : v);
}

function parseBody(v) {
  if (typeof v === 'string') {
    try { return JSON.parse(v); } catch (e) { return undefined; }
  }
  return v;
}

// ---------- SHA-256 (FIPS 180-4), UTF-8 input, hex output -------------------

const K = [
  0x428a2f98, 0x71374491, 0xb5c0fbcf, 0xe9b5dba5, 0x3956c25b, 0x59f111f1, 0x923f82a4, 0xab1c5ed5,
  0xd807aa98, 0x12835b01, 0x243185be, 0x550c7dc3, 0x72be5d74, 0x80deb1fe, 0x9bdc06a7, 0xc19bf174,
  0xe49b69c1, 0xefbe4786, 0x0fc19dc6, 0x240ca1cc, 0x2de92c6f, 0x4a7484aa, 0x5cb0a9dc, 0x76f988da,
  0x983e5152, 0xa831c66d, 0xb00327c8, 0xbf597fc7, 0xc6e00bf3, 0xd5a79147, 0x06ca6351, 0x14292967,
  0x27b70a85, 0x2e1b2138, 0x4d2c6dfc, 0x53380d13, 0x650a7354, 0x766a0abb, 0x81c2c92e, 0x92722c85,
  0xa2bfe8a1, 0xa81a664b, 0xc24b8b70, 0xc76c51a3, 0xd192e819, 0xd6990624, 0xf40e3585, 0x106aa070,
  0x19a4c116, 0x1e376c08, 0x2748774c, 0x34b0bcb5, 0x391c0cb3, 0x4ed8aa4a, 0x5b9cca4f, 0x682e6ff3,
  0x748f82ee, 0x78a5636f, 0x84c87814, 0x8cc70208, 0x90befffa, 0xa4506ceb, 0xbef9a3f7, 0xc67178f2
];

function utf8(str) {
  const out = [];
  for (let i = 0; i < str.length; i++) {
    let c = str.charCodeAt(i);
    if (c >= 0xd800 && c <= 0xdbff && i + 1 < str.length) {
      const d = str.charCodeAt(i + 1);
      if (d >= 0xdc00 && d <= 0xdfff) { c = 0x10000 + ((c - 0xd800) << 10) + (d - 0xdc00); i++; }
    }
    if (c < 0x80) { out.push(c); }
    else if (c < 0x800) { out.push(0xc0 | (c >> 6), 0x80 | (c & 63)); }
    else if (c < 0x10000) { out.push(0xe0 | (c >> 12), 0x80 | ((c >> 6) & 63), 0x80 | (c & 63)); }
    else { out.push(0xf0 | (c >> 18), 0x80 | ((c >> 12) & 63), 0x80 | ((c >> 6) & 63), 0x80 | (c & 63)); }
  }
  return out;
}

function sha256(str) {
  const bytes = utf8(str);
  const bitLen = bytes.length * 8;
  bytes.push(0x80);
  while (bytes.length % 64 !== 56) { bytes.push(0); }
  const hi = Math.floor(bitLen / 0x100000000);
  const lo = bitLen >>> 0;
  bytes.push((hi >>> 24) & 255, (hi >>> 16) & 255, (hi >>> 8) & 255, hi & 255,
    (lo >>> 24) & 255, (lo >>> 16) & 255, (lo >>> 8) & 255, lo & 255);
  const h = [0x6a09e667, 0xbb67ae85, 0x3c6ef372, 0xa54ff53a, 0x510e527f, 0x9b05688c, 0x1f83d9ab, 0x5be0cd19];
  const w = new Array(64);
  const rotr = (x, n) => (x >>> n) | (x << (32 - n));
  for (let off = 0; off < bytes.length; off += 64) {
    for (let i = 0; i < 16; i++) {
      w[i] = ((bytes[off + 4 * i] << 24) | (bytes[off + 4 * i + 1] << 16) | (bytes[off + 4 * i + 2] << 8) | bytes[off + 4 * i + 3]) >>> 0;
    }
    for (let i = 16; i < 64; i++) {
      const s0 = rotr(w[i - 15], 7) ^ rotr(w[i - 15], 18) ^ (w[i - 15] >>> 3);
      const s1 = rotr(w[i - 2], 17) ^ rotr(w[i - 2], 19) ^ (w[i - 2] >>> 10);
      w[i] = (w[i - 16] + s0 + w[i - 7] + s1) >>> 0;
    }
    let [a, b, c, d, e, f, g, hh] = h;
    for (let i = 0; i < 64; i++) {
      const S1 = rotr(e, 6) ^ rotr(e, 11) ^ rotr(e, 25);
      const ch = (e & f) ^ (~e & g);
      const t1 = (hh + S1 + ch + K[i] + w[i]) >>> 0;
      const S0 = rotr(a, 2) ^ rotr(a, 13) ^ rotr(a, 22);
      const maj = (a & b) ^ (a & c) ^ (b & c);
      const t2 = (S0 + maj) >>> 0;
      hh = g; g = f; f = e; e = (d + t1) >>> 0; d = c; c = b; b = a; a = (t1 + t2) >>> 0;
    }
    h[0] = (h[0] + a) >>> 0; h[1] = (h[1] + b) >>> 0; h[2] = (h[2] + c) >>> 0; h[3] = (h[3] + d) >>> 0;
    h[4] = (h[4] + e) >>> 0; h[5] = (h[5] + f) >>> 0; h[6] = (h[6] + g) >>> 0; h[7] = (h[7] + hh) >>> 0;
  }
  return h.map((x) => x.toString(16).padStart(8, '0')).join('');
}

function fingerprint(v) { return sha256(canonical(v)); }

// ---------- URL parsing (subset of Go net/url used by the tool) ------------

// Returns null when unparseable (including invalid %-escapes, as Go does).
function parseURL(raw) {
  if (typeof raw !== 'string') { return null; }
  const m = /^([A-Za-z][A-Za-z0-9+.-]*):\/\/([^/?#]*)([^?#]*)(\?[^#]*)?(#.*)?$/.exec(raw);
  if (!m) { return null; }
  const authority = m[2];
  const at = authority.lastIndexOf('@');
  // Like Go's url.URL.Path, path is percent-decoded; rawPath keeps the original.
  let path;
  try { path = decodeURIComponent(m[3]); } catch (e) { return null; }
  return {
    scheme: m[1].toLowerCase(),
    user: at >= 0,
    host: at >= 0 ? authority.slice(at + 1) : authority,
    path: path,
    rawPath: m[3],
    query: m[4] === undefined ? '' : m[4].slice(1),
    hasQuery: m[4] !== undefined,
    fragment: m[5] === undefined ? '' : m[5].slice(1),
    hasFragment: m[5] !== undefined
  };
}

function trimRightSlash(p) { return p.replace(/\/+$/, ''); }
function trimSlashes(p) { return p.replace(/^\/+/, '').replace(/\/+$/, ''); }
function eqFold(a, b) { return String(a).toLowerCase() === String(b).toLowerCase(); }

// ---------- redaction (promotion.Redact) ------------------------------------

function redact(v) {
  if (Array.isArray(v)) { return v.map(redact); }
  if (!isObject(v)) { return v; }
  const out = {};
  for (const k of Object.keys(v)) {
    const lk = k.replace(/_/g, '').replace(/-/g, '').toLowerCase();
    if (lk === 'issensitive') { out[k] = v[k]; continue; }
    if (lk.indexOf('password') >= 0 || lk.indexOf('secret') >= 0 || lk.indexOf('token') >= 0 ||
        lk.indexOf('key') >= 0 || lk.indexOf('credential') >= 0 || lk.indexOf('authentication') >= 0 ||
        lk === 'auth' || (lk === 'value' && v.isSensitive === true)) {
      out[k] = '[REDACTED]';
    } else {
      out[k] = redact(v[k]);
    }
  }
  return out;
}

// ---------- inventories (pingone.Inventory / InventoryVersion) --------------

function pinnedId(s) { return typeof s === 'string' && s !== '' && s !== '.' && s !== '..' && /^[A-Za-z0-9_-]+$/.test(s); }

// Build an inventory from the environment record, the snapshot version record
// and every page of embeddedSnapshots. pinned=true applies InventoryVersion's
// stricter rules (exact snapshot ID, COMPLETE, URL ends in the resource ID).
function inventoryFromSnapshots(apiOrigin, env, envRecord, snapshot, embeddedPages, pinned, expectedSnapshotId) {
  if (!isObject(envRecord) || envRecord.id !== env || !envRecord.organization || !envRecord.organization.id) {
    throw new Error('environment response omitted its identity or organization');
  }
  if (!isObject(snapshot) || !snapshot.snapshotId || !snapshot.resource || snapshot.resource.id !== env) {
    throw new Error('snapshot response omitted pinned environment/version identity');
  }
  if (snapshot.status !== 'COMPLETE') { throw new Error('snapshot is not COMPLETE'); }
  if (pinned && (snapshot.snapshotId !== expectedSnapshotId || !pinnedId(env) || !pinnedId(expectedSnapshotId))) {
    throw new Error('pinned snapshot identity or COMPLETE status did not match');
  }
  const inv = { apiOrigin: apiOrigin, environmentId: env, organizationId: envRecord.organization.id, snapshotId: snapshot.snapshotId, resources: [] };
  const seen = {};
  const prefix = '/v1/environments/' + env + '/';
  for (const page of embeddedPages) {
    const items = page && page._embedded && page._embedded.referencedResources;
    if (!Array.isArray(items)) { throw new Error('incomplete PingOne response: missing referencedResources collection'); }
    for (const s of items) {
      if (!isObject(s) || !isObject(s.configuration)) { throw new Error('inventory contains deleted or unavailable configuration; resolve it before promotion'); }
      if (s.status && s.status !== 'COMPLETE') { throw new Error('inventory contains an incomplete resource snapshot'); }
      const u = apiURL(apiOrigin, s.resourceUrl);
      if (!u) { throw new Error('refused API URL outside the configured PingOne origin or environment path'); }
      const rid = s.resource && s.resource.id;
      if (u.path.indexOf(prefix) !== 0 || !rid || u.hasQuery || (pinned && (!pinnedId(rid) || u.path.indexOf('//') >= 0))) {
        throw new Error('inventory has an invalid resource identity');
      }
      if (seen[rid]) { throw new Error('ambiguous resource ID ' + rid); }
      seen[rid] = true;
      const parts = trimSlashes(u.path.slice(prefix.length)).split('/');
      if (pinned && (parts.length < 2 || parts[parts.length - 1] !== rid)) { throw new Error('pinned resource URL does not match its original identity'); }
      let type = parts[0];
      if (parts.length > 2 || pinned) {
        const kinds = [];
        for (let i = 0; i < parts.length; i += 2) {
          if (pinned && !pinnedId(parts[i])) { throw new Error('invalid pinned resource path'); }
          kinds.push(parts[i]);
        }
        type = kinds.join(' / ');
      }
      let name = typeof s.configuration.name === 'string' ? s.configuration.name : '';
      if (!name) { name = typeof s.configuration.displayName === 'string' ? s.configuration.displayName : ''; }
      if (!name) { name = rid; }
      inv.resources.push({ id: rid, url: apiOrigin + u.path, name: name, type: type, configuration: s.configuration,
        dependencies: Array.isArray(s.referencedResources) ? s.referencedResources : [] });
    }
  }
  return inv;
}

// pingone.Client.apiURL: resolve against the API origin (dot segments
// removed, as Go's ResolveReference does) and refuse anything outside
// /v1/environments/ on that exact origin, or any path Go would %-escape.
function removeDotSegments(path) {
  const out = [];
  const segs = path.split('/');
  for (let i = 0; i < segs.length; i++) {
    const seg = segs[i];
    if (seg === '.') { if (i === segs.length - 1) { out.push(''); } continue; }
    if (seg === '..') { if (out.length > 1) { out.pop(); } if (i === segs.length - 1) { out.push(''); } continue; }
    out.push(seg);
  }
  return out.join('/');
}

function apiURL(apiOrigin, raw) {
  const base = parseURL(apiOrigin);
  if (!base || typeof raw !== 'string') { return null; }
  let u;
  if (raw.charAt(0) === '/' && raw.charAt(1) !== '/') {
    u = parseURL(base.scheme + '://' + base.host + raw);
  } else {
    u = parseURL(raw);
  }
  if (!u || u.scheme !== base.scheme || u.host !== base.host || u.user || u.hasFragment) { return null; }
  if (!/^[A-Za-z0-9\-._~!$&'()*+,;=:@/]*$/.test(u.rawPath)) { return null; }
  u.path = removeDotSegments(u.path);
  if (u.path.indexOf('/v1/environments/') !== 0) { return null; }
  return u;
}

function nextPageURL(page) {
  const href = page && page._links && page._links.next && page._links.next.href;
  return typeof href === 'string' ? href : '';
}

// Pages() completeness rule: the summed items must reach the largest count seen.
function pagesComplete(pages) {
  let expected = -1;
  let total = 0;
  for (const p of pages) {
    const items = p && p._embedded && p._embedded.referencedResources;
    total += Array.isArray(items) ? items.length : 0;
    if (p && typeof p.count === 'number') { expected = Math.max(expected, p.count); }
  }
  return expected < 0 || total >= expected;
}

function index(inv) {
  const m = {};
  for (const r of inv.resources) { m[r.id] = r; }
  return m;
}

// ---------- mapping suggestions (internal/app/workflow.go suggestMappings) --
// Saved PingOne promotion mappings first (same type, each destination used
// once), then a unique same-type match by trimmed, case-insensitive name.
function suggestMappings(source, target, native) {
  const dst = index(target);
  const nat = isObject(native) ? native : {};
  const out = {};
  const used = {};
  for (const r of source.resources) {
    const d = Object.prototype.hasOwnProperty.call(nat, r.id) ? dst[nat[r.id]] : undefined;
    if (d && d.type === r.type && !used[d.id]) { out[r.id] = d.id; used[d.id] = true; }
  }
  for (const r of source.resources) {
    if (out[r.id]) { continue; }
    const matches = target.resources.filter((d) => r.type === d.type && eqFold(String(r.name).trim(), String(d.name).trim())).map((d) => d.id);
    if (matches.length === 1 && !used[matches[0]]) { out[r.id] = matches[0]; used[matches[0]] = true; }
  }
  return out;
}

// ---------- request body (promotion.BuildRequest) --------------------------

function buildRequest(source, target, selected, mappings, description) {
  if (!selected || selected.length === 0) { throw new Error('selection must not be empty'); }
  if (!source.environmentId || !target.environmentId || source.environmentId === target.environmentId) {
    throw new Error('source and target environments must be distinct and known');
  }
  if (!source.snapshotId) { throw new Error('source snapshot must be pinned'); }
  if (!source.organizationId || !target.organizationId || source.organizationId !== target.organizationId) {
    throw new Error('source and target must belong to the same organization');
  }
  const src = index(source);
  const dst = index(target);
  const sel = {};
  for (const id of selected) {
    if (!src[id]) { throw new Error('unknown source resource "' + id + '"'); }
    if (sel[id]) { throw new Error('duplicate selected resource "' + id + '"'); }
    sel[id] = true;
  }
  const seenTargets = {};
  for (const sid of Object.keys(mappings || {})) {
    const tid = mappings[sid];
    if (!src[sid]) { throw new Error('mapping source "' + sid + '" is unknown'); }
    if (!dst[tid]) { throw new Error('mapping target "' + tid + '" is unknown'); }
    if (src[sid].type !== dst[tid].type) { throw new Error('mapping "' + sid + '" to "' + tid + '" has incompatible resource types'); }
    if (seenTargets[tid] !== undefined && seenTargets[tid] !== sid) { throw new Error('multiple sources map to target "' + tid + '"'); }
    seenTargets[tid] = sid;
  }
  const excluded = Object.keys(src).filter((id) => !sel[id]).sort();
  return {
    sourceSnapshot: { id: source.snapshotId },
    targetEnvironment: { id: target.environmentId },
    selectedResources: selected.slice().sort(),
    excludedResources: excluded,
    resourceMapping: mappings || {},
    description: description
  };
}

// ---------- Go-compatible JSON (encoding/json Marshal / MarshalIndent) -----
// Fingerprints in request.json are SHA-256 over Go's exact encoding, so this
// reproduces it: map keys sorted by UTF-8 bytes; <, >, & and U+2028/2029
// escaped; control characters as \b \f \n \r \t (Go 1.22+) or \u00XX; lone surrogates (invalid
// UTF-8 in Go) become U+FFFD; float64 formatting as strconv with Go's
// exponent thresholds; -0 kept.

function utf8Compare(a, b) {
  const x = utf8(a);
  const y = utf8(b);
  const n = Math.min(x.length, y.length);
  for (let i = 0; i < n; i++) { if (x[i] !== y[i]) { return x[i] - y[i]; } }
  return x.length - y.length;
}

function goString(s) {
  let out = '"';
  for (let i = 0; i < s.length; i++) {
    let c = s.charCodeAt(i);
    if (c >= 0xd800 && c <= 0xdbff) {
      const d = i + 1 < s.length ? s.charCodeAt(i + 1) : 0;
      if (d >= 0xdc00 && d <= 0xdfff) { out += s.charAt(i) + s.charAt(i + 1); i++; continue; }
      out += '�';
      continue;
    }
    if (c >= 0xdc00 && c <= 0xdfff) { out += '�'; continue; }
    if (c === 0x22) { out += '\\"'; }
    else if (c === 0x5c) { out += '\\\\'; }
    else if (c === 0x0a) { out += '\\n'; }
    else if (c === 0x0d) { out += '\\r'; }
    else if (c === 0x09) { out += '\\t'; }
    else if (c === 0x08) { out += '\\b'; }
    else if (c === 0x0c) { out += '\\f'; }
    else if (c < 0x20 || c === 0x3c || c === 0x3e || c === 0x26 || c === 0x2028 || c === 0x2029) {
      out += '\\u' + c.toString(16).padStart(4, '0');
    } else { out += s.charAt(i); }
  }
  return out + '"';
}

function goNumber(v) {
  if (!isFinite(v)) { throw new Error('unsupported number in configuration'); }
  if (v === 0) { return Object.is(v, -0) ? '-0' : '0'; }
  const abs = Math.abs(v);
  if (abs < 1e-6 || abs >= 1e21) {
    // strconv 'e' with shortest digits; Go writes e-07 then trims to e-7, as JS does.
    return v.toExponential().replace(/e([+-])(\d)$/, 'e$10$2').replace(/e([+-])0(\d)$/, 'e$1$2');
  }
  return String(v);
}

function goJSON(v, indent, level) {
  const nl = indent ? '\n' + indent.repeat(level + 1) : '';
  const end = indent ? '\n' + indent.repeat(level) : '';
  const sep = indent ? ': ' : ':';
  if (v === null || v === undefined) { return 'null'; }
  if (typeof v === 'boolean') { return v ? 'true' : 'false'; }
  if (typeof v === 'number') { return goNumber(v); }
  if (typeof v === 'string') { return goString(v); }
  if (Array.isArray(v)) {
    if (v.length === 0) { return '[]'; }
    return '[' + v.map((x) => nl + goJSON(x, indent, level + 1)).join(',') + end + ']';
  }
  if (v.__goOrder) {
    // Struct: fixed field order, like a Go struct.
    const keys = v.__goOrder;
    if (keys.length === 0) { return '{}'; }
    return '{' + keys.map((k) => nl + goString(k) + sep + goJSON(v[k], indent, level + 1)).join(',') + end + '}';
  }
  const keys = Object.keys(v).sort(utf8Compare);
  if (keys.length === 0) { return '{}'; }
  return '{' + keys.map((k) => nl + goString(k) + sep + goJSON(v[k], indent, level + 1)).join(',') + end + '}';
}

function goStruct(order, values) {
  const o = Object.assign({}, values);
  Object.defineProperty(o, '__goOrder', { value: order, enumerable: false });
  return o;
}

// gitops.fingerprint: SHA-256 of json.Marshal(redacted configuration).
function goFingerprint(v) { return sha256(goJSON(v, '', 0)); }

// ---------- request.json (gitops.NewRequest / Validate / Encode) ------------

const HEX_ID = /^[a-f0-9]{32}$/;
const HASH_ID = /^[a-f0-9]{64}$/;
const REGIONS = { us: 1, eu: 1, ca: 1, au: 1, sg: 1, asia: 1 };
const RFC3339 = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d{1,9})?(Z|[+-]\d{2}:\d{2})$/;

function validateRequest(r) {
  if (r.version !== 1 || !HEX_ID.test(r.id)) { throw new Error('unsupported request version or invalid request identity'); }
  if (!RFC3339.test(r.createdAt)) { throw new Error('invalid request creation time'); }
  if (utf8(r.description).length > 4000) { throw new Error('request description is too long'); }
  for (const env of [r.source, r.target]) {
    if (!SAFE_ID.test(env.id) || !SAFE_ID.test(env.organizationId) || !SAFE_ID.test(env.snapshotId) || !REGIONS[env.region]) {
      throw new Error('request requires safe environment, region, organization and pinned snapshot identities');
    }
  }
  if (r.source.id === r.target.id || r.source.organizationId !== r.target.organizationId) {
    throw new Error('source and target must be distinct environments in the same organization');
  }
  const n = r.selected.length;
  if (n === 0 || n > 10000 || Object.keys(r.configurations).length !== n || Object.keys(r.fingerprints).length !== n) {
    throw new Error('request selection and configuration/fingerprint sets must match exactly');
  }
  const seen = {};
  for (const id of r.selected) {
    if (!SAFE_ID.test(id) || seen[id]) { throw new Error('invalid or duplicate selected resource identity'); }
    seen[id] = true;
    const config = r.configurations[id];
    if (!isObject(config) || Object.keys(config).length === 0 || !HASH_ID.test(r.fingerprints[id] || '') || goFingerprint(config) !== r.fingerprints[id]) {
      throw new Error('selected resource "' + id + '" has missing or mismatched configuration fingerprint');
    }
    if (canonical(config) !== canonical(redact(config))) { throw new Error('configuration contains unredacted sensitive fields'); }
  }
  const targets = {};
  for (const k of Object.keys(r.mappings)) {
    const v = r.mappings[k];
    if (!SAFE_ID.test(k) || !SAFE_ID.test(v) || targets[v]) { throw new Error('invalid or conflicting resource mapping'); }
    targets[v] = true;
  }
}

// Build request.json exactly as the Go app's NewRequest + Encode (+ "\n" on
// commit). id and createdAt are supplied (random ID, RFC3339 UTC time).
function newRequest(source, target, sourceRegion, targetRegion, selected, mappings, description, id, createdAt) {
  buildRequest(source, target, selected, mappings, description);
  const src = index(source);
  const configurations = {};
  const fingerprints = {};
  for (const sid of selected) {
    const res = src[sid];
    if (!res || !isObject(res.configuration) || Object.keys(res.configuration).length === 0) {
      throw new Error('selected resource has no available configuration');
    }
    const redacted = redact(res.configuration);
    configurations[sid] = redacted;
    fingerprints[sid] = goFingerprint(redacted);
  }
  const r = encodeOrder({
    version: 1, id: id, createdAt: createdAt, description: description,
    source: { id: source.environmentId, region: sourceRegion, organizationId: source.organizationId, snapshotId: source.snapshotId },
    target: { id: target.environmentId, region: targetRegion, organizationId: target.organizationId, snapshotId: target.snapshotId },
    selected: selected.slice().sort(utf8Compare), mappings: Object.assign({}, mappings || {}),
    configurations: configurations, fingerprints: fingerprints
  });
  validateRequest(r);
  return { request: r, json: encodeRequest(r) };
}

// gitops.Request field order, for building or for re-encoding a parsed request.
function encodeOrder(r) {
  const env = (e) => goStruct(['id', 'region', 'organizationId', 'snapshotId'], e || {});
  return goStruct(['version', 'id', 'createdAt', 'description', 'source', 'target', 'selected', 'mappings', 'configurations', 'fingerprints'],
    Object.assign({}, r, { source: env(r.source), target: env(r.target) }));
}

// gitops.Encode: MarshalIndent with two spaces, at most 8 MiB.
function encodeRequest(r) {
  const raw = goJSON(encodeOrder(r), '  ', 0);
  if (utf8(raw).length > (8 << 20)) { throw new Error('request exceeds 8 MiB'); }
  return raw;
}

// Go slices strings by bytes: title[:180] may cut a character, which Go's
// encoder then writes as U+FFFD.
function truncateUtf8(s, max) {
  const b = utf8(s);
  if (b.length <= max) { return s; }
  let out = '';
  let i = 0;
  const cut = b.slice(0, max);
  while (i < cut.length) {
    const c = cut[i];
    const len = c < 0x80 ? 1 : c < 0xe0 ? 2 : c < 0xf0 ? 3 : 4;
    // Go's encoder writes one U+FFFD per byte of an incomplete sequence.
    if (i + len > cut.length) { out += '�'.repeat(cut.length - i); break; }
    let cp = len === 1 ? c : len === 2 ? c & 0x1f : len === 3 ? c & 0x0f : c & 0x07;
    for (let k = 1; k < len; k++) { cp = (cp << 6) | (cut[i + k] & 0x3f); }
    out += String.fromCodePoint(cp);
    i += len;
  }
  return out;
}

// Go's time.RFC3339Nano in UTC: fraction with trailing zeros removed.
function rfc3339Nano(date) {
  return date.toISOString().replace(/\.(\d*?)0+Z$/, (m, f) => (f ? '.' + f + 'Z' : 'Z'));
}

// ---------- GitLab submit (gitlab.Config.Validate, VerifyProject, Submit) ---

const NUMERIC_ID = /^[0-9]+$/;
const SAFE_ENVIRONMENT = /^[A-Za-z0-9][A-Za-z0-9_-]{0,62}$/;

function noTraversal(rawPath) {
  for (const part of rawPath.split('/')) {
    let d;
    try { d = decodeURIComponent(part); } catch (e) { return false; }
    if (d === '.' || d === '..') { return false; }
  }
  return true;
}

// Returns {base, api, environment}; api is "<base>/api/v4/projects/<id>".
function gitlabConfig(baseUrl, projectId, environment) {
  const raw = String(baseUrl || '').trim() === '' ? '' : String(baseUrl);
  if (!raw) { throw new Error('GitLab base URL is required'); }
  const u = parseURL(raw);
  if (!u || u.scheme !== 'https' || u.host === '' || u.user || u.hasQuery || u.hasFragment || raw.indexOf('\\') >= 0) {
    throw new Error('GitLab base URL must be HTTPS without credentials, query, or fragment');
  }
  if (!noTraversal(u.rawPath)) { throw new Error('GitLab base URL cannot contain traversal'); }
  if (!NUMERIC_ID.test(String(projectId || ''))) { throw new Error('GitLab project ID must be numeric'); }
  const env = String(environment || '') || 'production';
  if (!SAFE_ENVIRONMENT.test(env)) { throw new Error('GitLab environment name is invalid'); }
  const prefix = u.scheme + '://' + u.host + trimRightSlash(u.rawPath);
  return { base: u, api: prefix + '/api/v4/projects/' + projectId, projectId: String(projectId), environment: env };
}

function sameGitLabLocation(base, raw) {
  const u = parseURL(raw);
  if (!base || !u || u.scheme !== 'https' || !eqFold(u.host, base.host) || u.user || u.hasQuery || u.hasFragment) { return false; }
  if (!noTraversal(u.rawPath)) { return false; }
  const bp = trimRightSlash(base.path);
  return u.path === bp || u.path.indexOf(bp + '/') === 0;
}

function gitlabStatus(statusCode, what) {
  const s = Number(statusCode);
  if (!(s >= 200 && s < 300)) { throw new Error(what + ': GitLab returned HTTP ' + (isFinite(s) ? s : 'error')); }
}

// VerifyProject, first half. Returns the default branch and its escaped form.
function checkProject(cfg, statusCode, body) {
  gitlabStatus(statusCode, 'verify GitLab project');
  const p = parseBody(body);
  if (!isObject(p)) { throw new Error('verify GitLab project: GitLab returned invalid JSON'); }
  const branch = typeof p.default_branch === 'string' ? p.default_branch : '';
  if (p.visibility !== 'private' || p.archived === true || branch.trim() === '') {
    throw new Error('GitLab project must be private, active, and have a default branch');
  }
  if (typeof p.id !== 'number' || String(p.id) !== String(Number(cfg.projectId))) { throw new Error('GitLab returned a different project than configured'); }
  if (typeof p.web_url !== 'string' || !sameGitLabLocation(cfg.base, p.web_url)) {
    throw new Error('GitLab returned a project URL outside the configured server path');
  }
  return { defaultBranch: branch, defaultBranchPath: encodeURIComponent(branch) };
}

// VerifyProject, second half.
function checkDefaultBranch(defaultBranch, statusCode, body) {
  gitlabStatus(statusCode, 'verify GitLab default branch');
  const b = parseBody(body);
  if (!isObject(b) || b.name !== defaultBranch || b.protected !== true) { throw new Error('GitLab default branch must be protected'); }
}

// The three Submit payloads, the MR title and description, exactly as the
// Go app builds them. requestedBy adds one line naming the signed-in admin.
function submitBodies(request, requestJson, environment, requestedBy, extra) {
  const id = request.id;
  const branch = 'promotion/' + id;
  let title = request.description !== '' ? request.description : 'Promote selected PingOne resources';
  title = truncateUtf8(title, 180);
  let description = 'Promotion request `' + id + '`\n\nSource environment: `' + request.source.id + '` (snapshot `' + request.source.snapshotId +
    '`)\nTarget environment: `' + request.target.id + '`\nSelected resources: ' + request.selected.length + '\nGitLab environment: `' + environment +
    '`\n\nThis merge request submits configuration for review. Actual deployment and rollback must execute through protected GitLab jobs. ' +
    'Review the configuration exports and final native plan before approving deployment.';
  if (requestedBy) { description += '\n\nRequested in DaVinci by: `' + requestedBy + '`'; }
  if (extra) { description += String(extra); }
  return {
    branch: branch,
    title: title,
    description: description,
    branchBody: (defaultBranch) => goJSON({ branch: branch, ref: defaultBranch }, '', 0),
    commitBody: (defaultBranch) => goJSON({
      actions: [{ action: 'create', content: requestJson + '\n', file_path: 'requests/' + id + '/request.json' }],
      branch: branch, commit_message: 'Add promotion request ' + id, start_branch: defaultBranch
    }, '', 0),
    mrBody: (defaultBranch) => goJSON({ description: description, remove_source_branch: true, source_branch: branch, squash: false,
      target_branch: defaultBranch, title: title }, '', 0)
  };
}

function checkMergeRequest(cfg, statusCode, body) {
  gitlabStatus(statusCode, 'create merge request');
  const mr = parseBody(body);
  if (!isObject(mr) || typeof mr.web_url !== 'string' || !sameGitLabLocation(cfg.base, mr.web_url)) {
    throw new Error('GitLab returned a merge request URL outside the configured server path');
  }
  return { webUrl: mr.web_url, iid: typeof mr.iid === 'number' ? mr.iid : 0 };
}

// ---------- promotion variables (advisory pre-check; not in the Go app) ----
// PingOne requires a sensitive promotion variable for every secret attribute
// of a promoted resource. The request already redacts those attributes, so
// every "[REDACTED]" value marks one. Declarations (per resource, source
// environment) map a property to a variable; definitions hold the values.

function sensitivePaths(v, prefix, out) {
  out = out || [];
  if (v === '[REDACTED]') { out.push(prefix); return out; }
  if (Array.isArray(v)) { v.forEach((x, i) => sensitivePaths(x, prefix + '[' + i + ']', out)); return out; }
  if (isObject(v)) {
    for (const k of Object.keys(v).sort(utf8Compare)) { sensitivePaths(v[k], prefix ? prefix + '.' + k : k, out); }
  }
  return out;
}

function variableName(ref) {
  const m = /^\$\{\s*promotionVariable\s*:\s*([^}]*?)\s*\}$/.exec(String(ref || ''));
  return m ? m[1] : String(ref || '');
}

function definitionsFrom(pages) {
  const defs = {};
  for (const p of Array.isArray(pages) ? pages : []) {
    for (const d of ((p && p._embedded && p._embedded.referencedResources) || [])) {
      if (d && typeof d.name === 'string') {
        defs[d.name] = { sensitive: d.isSensitive === true || d.variableType === 'ENCRYPTED_STRING' || d.value === '******' };
      }
    }
  }
  return defs;
}

// A declared property covers a sensitive path when it names that path or an
// ancestor of it ("properties" covers "properties.clientSecret.value").
function covers(prop, path) {
  return prop === path || path.indexOf(prop + '.') === 0 || path.indexOf(prop + '[') === 0;
}

function md(s) { return String(s).replace(/[`\r\n]/g, ''); }

// decls: [{resourceId, status, body}] for each selected source resource.
// Returns per-attribute findings plus page text and an MR section.
function variablesReport(request, decls, srcPages, srcError, tgtPages, tgtError) {
  const src = definitionsFrom(srcPages);
  const tgt = definitionsFrom(tgtPages);
  const byId = {};
  for (const d of Array.isArray(decls) ? decls : []) { if (d && typeof d.resourceId === 'string') { byId[d.resourceId] = d; } }
  const findings = [];
  const notes = [];
  let unreadable = 0;
  for (const id of request.selected) {
    const paths = sensitivePaths(request.configurations[id], '');
    if (paths.length === 0) { continue; }
    const d = byId[id] || { status: 0 };
    const status = Number(d.status);
    let vars = {};
    if (status === 200) {
      const body = parseBody(d.body) || {};
      vars = isObject(body.variables) ? body.variables : {};
    } else if (status !== 404) {
      unreadable++;
    }
    for (const path of paths) {
      const f = { resourceId: id, path: path, variable: '', state: '' };
      const prop = Object.keys(vars).sort(utf8Compare).find((p) => covers(p, path));
      if (status !== 200 && status !== 404) {
        f.state = 'unknown';
      } else if (prop === undefined) {
        f.state = 'missing';
      } else {
        f.variable = variableName(vars[prop]);
        const def = tgt[f.variable] || src[f.variable];
        if (!def) { f.state = (srcError || tgtError) ? 'unknown' : 'undefined'; }
        else if (!def.sensitive) { f.state = 'not-sensitive'; }
        else { f.state = 'ready'; }
      }
      findings.push(f);
    }
  }
  if (unreadable) { notes.push(unreadable + ' resource declaration(s) could not be read.'); }
  if (srcError) { notes.push('Source variable definitions could not be read: ' + srcError); }
  if (tgtError) { notes.push('Target variable definitions could not be read: ' + tgtError); }
  const say = {
    ready: 'ready: sensitive promotion variable {v} is defined',
    missing: 'needs a sensitive promotion variable; none is declared for it',
    undefined: 'declared as {v}, but no definition was found in source or target',
    'not-sensitive': 'declared as {v}, but its definition is not marked sensitive',
    unknown: 'could not be checked'
  };
  const line = (f) => f.resourceId + '  ' + f.path + ': ' + say[f.state].replace('{v}', f.variable);
  const open = findings.filter((f) => f.state !== 'ready').length;
  let summary;
  if (findings.length === 0) {
    summary = 'No sensitive values in the selected resources; no promotion variables are needed.';
  } else if (open === 0) {
    summary = 'All ' + findings.length + ' sensitive value(s) have a sensitive promotion variable.';
  } else {
    summary = open + ' of ' + findings.length + ' sensitive value(s) need attention in PingOne (Configuration Management, promotion variables) before deployment, or the GitLab plan will stop.';
  }
  const shown = findings.slice(0, 200);
  if (findings.length > shown.length) { notes.push('Showing the first 200 of ' + findings.length + ' sensitive values.'); }
  const text = [summary].concat(notes, shown.map(line)).join('\n');
  let mrText = '\n\n### Promotion variables (advisory check by the DaVinci flow)\n\n' + md(summary);
  for (const n of notes) { mrText += '\n\n' + md(n); }
  for (const f of shown) {
    mrText += '\n- `' + md(f.resourceId) + '` `' + md(f.path) + '`: ' + say[f.state].replace('{v}', '`' + md(f.variable) + '`');
  }
  return { findings: findings, open: open, summaryText: summary, reportText: text, mrText: mrText };
}

// 16 random bytes as hex, like Go's crypto/rand request ID. Uses the Web
// Crypto global when the runtime has one; otherwise SHA-256 over every
// source of variation available. The ID needs to be unique, not secret.
function randomHexId(extra) {
  const g = typeof globalThis !== 'undefined' ? globalThis.crypto : undefined;
  if (g && typeof g.getRandomValues === 'function') {
    const b = new Uint8Array(16);
    g.getRandomValues(b);
    return Array.from(b, (x) => x.toString(16).padStart(2, '0')).join('');
  }
  let seed = String(extra || '') + '|' + Date.now();
  for (let i = 0; i < 8; i++) { seed += '|' + Math.random(); }
  return sha256(seed).slice(0, 32);
}

function escapeHtml(s) {
  return String(s).replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
}

const core = {
  rfc3339Nano, gitlabConfig, sameGitLabLocation, gitlabStatus, checkProject, checkDefaultBranch, submitBodies, checkMergeRequest,
  SAFE_ID, apiURL, encodeRequest, goJSON, goFingerprint, newRequest, validateRequest, truncateUtf8, utf8Compare, HEX_ID, canonical, parseBody, sha256, fingerprint, parseURL, redact, inventoryFromSnapshots, nextPageURL,
  pagesComplete, buildRequest, suggestMappings, escapeHtml, randomHexId, sensitivePaths, variableName, variablesReport
};
