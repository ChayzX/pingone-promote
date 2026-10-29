// Drives the "Choose resources" screen in a real browser: the page HTML, CSS
// and Script fields exactly as exported, with picker data from the inventory
// step. Checks the behaviour of the local app's Step 2: search, type filter,
// selection count, dependencies, destination mappings with suggestions, and
// the hidden selectedRaw / mappingsRaw fields DaVinci submits.
// usage: node test/picker_ui.js export/pingone-promote-request.flow.json [screenshot.png]
const fs = require('fs');
const assert = require('assert');
let chromium;
try { ({ chromium } = require('playwright')); } catch (e) { console.log('picker ui: playwright not installed, skipped'); process.exit(0); }
const core = require('./load_core');

const data = JSON.parse(fs.readFileSync(process.argv[2], 'utf8'));
const node = (data.flows || [data]).flatMap((f) => f.graphData.elements.nodes).map((n) => n.data)
  .find((d) => d.properties && d.properties.nodeTitle && d.properties.nodeTitle.value === 'Choose resources');
const p = node.properties;
const src = { resources: [
  { id: 'f1', name: 'Login', type: 'flows', dependencies: ['c1', 'missing-x'] },
  { id: 'f2', name: 'Registration', type: 'flows', dependencies: ['c1'] },
  { id: 'c1', name: 'HTTP connection', type: 'connections', dependencies: [] },
  { id: 'a1', name: '<img src=x onerror=window.pwned=1>', type: 'applications', dependencies: [] },
  { id: 'a2', name: 'Customer portal', type: 'applications', dependencies: [] }] };
const tgt = { resources: [
  { id: 't-f1', name: 'login', type: 'flows' }, { id: 't-f9', name: 'Old login', type: 'flows' },
  { id: 't-a2', name: 'Customer portal', type: 'applications' }] };
const picker = { source: src.resources, target: tgt.resources, suggestions: core.suggestMappings(src, tgt, {}),
  warnings: ['Saved PingOne mappings could not be read. Review suggestions and the final native plan: HTTP 404.'] };
const values = { pickerData: core.escapeHtml(JSON.stringify(picker)), sourceEnvName: 'Dev', targetEnvName: 'Prod',
  sourceListText: 'flows f1 Login', targetListText: 'flows t-f1 login', usernameText: 'alice' };
const html = p.customHTML.value.replace(/\{\{([^}]+)\}\}/g, (m, ref) => { const k = ref.split('.').pop(); return values[k] !== undefined ? values[k] : ''; });

let n = 0;
const ok = (cond, msg) => { assert.ok(cond, msg); n++; };
(async () => {
  const browser = await chromium.launch();
  const page = await browser.newPage({ viewport: { width: 1360, height: 1000 } });
  await page.setContent('<!doctype html><html><head><style>' + p.customCSS.value + '</style></head><body style="margin:0">' + html + '</body></html>');
  // DaVinci may run a script more than once; the picker must build only one table.
  await page.evaluate((s) => { (0, eval)(s); (0, eval)(s); }, p.customScript.value);
  await page.waitForSelector('#ppRows tr');
  const fields = () => page.evaluate(() => ({ sel: document.getElementById('selectedRaw').value, map: document.getElementById('mappingsRaw').value,
    count: document.getElementById('ppCount').textContent, total: document.getElementById('ppTotal').textContent,
    build: document.querySelector('[data-skbuttonvalue="BUILD"]').disabled, rows: document.querySelectorAll('#ppRows tr').length }));
  let f = await fields();
  ok(f.rows === 5 && f.total === '5 of 5 resources' && f.count === '0 selected' && f.build, 'all resources listed once, nothing selected, Build disabled: ' + JSON.stringify(f));
  ok(await page.evaluate(() => document.getElementById('ppManual').hidden && document.getElementById('ppManualFields').hidden), 'plain fallback hidden once the picker runs');
  ok(f.map.split('\n').sort().join() === 'a2=t-a2,f1=t-f1', 'suggested mappings (unique same-type name) prefilled for every resource, as the local app sends them: ' + f.map);
  ok(await page.evaluate(() => /Mapping suggestions need attention/.test(document.getElementById('ppWarn').textContent)), 'mapping warnings shown');
  ok(await page.evaluate(() => !window.pwned && document.body.innerHTML.indexOf('&lt;img src=x') >= 0), 'resource names are shown as text, never as HTML');
  await page.fill('#ppSearch', 'LOGIN');
  f = await fields();
  ok(f.rows === 1 && f.total === '1 of 5 resources', 'search matches name, type or ID, any case');
  await page.fill('#ppSearch', 'c1');
  ok((await fields()).rows === 1, 'search by ID');
  await page.fill('#ppSearch', '');
  await page.selectOption('#ppType', 'flows');
  ok((await fields()).rows === 2, 'type filter');
  await page.check('#ppRows tr:nth-child(1) input[type=checkbox]');
  f = await fields();
  ok(f.sel === 'f1' && f.count === '1 selected' && !f.build, 'checking a row selects it and enables Build');
  await page.click('#ppRows tr:nth-child(1) .pp-deps button');
  ok(await page.evaluate(() => document.querySelector('#ppRows tr:nth-child(1) .pp-deps').textContent.indexOf('HTTP connection') >= 0), 'dependencies listed by name');
  ok(await page.evaluate(() => [...document.querySelectorAll('#ppRows tr:nth-child(1) .pp-deps button')].find((b) => /Select listed/.test(b.textContent)).disabled),
    'Select listed dependencies is disabled when one is missing from the inventory');
  await page.click('#ppRows tr:nth-child(2) .pp-deps button');
  await page.evaluate(() => [...document.querySelectorAll('#ppRows tr:nth-child(2) .pp-deps button')].find((b) => /Select listed/.test(b.textContent)).click());
  f = await fields();
  ok(f.sel.split('\n').sort().join() === 'c1,f1', 'Select listed dependencies adds them (even when filtered out of view)');
  await page.selectOption('#ppRows tr:nth-child(1) select.pp-map', 't-f9');
  ok((await fields()).map.split('\n').sort().join() === 'a2=t-a2,f1=t-f9', 'choosing a destination overrides the suggestion');
  await page.selectOption('#ppRows tr:nth-child(1) select.pp-map', '');
  ok((await fields()).map === 'a2=t-a2', 'Automatic / create if unmatched removes the mapping');
  await page.selectOption('#ppType', '');
  await page.click('#ppClear');
  f = await fields();
  ok(f.sel === '' && f.count === '0 selected' && f.build, 'Clear selection');
  await page.check('#ppRows tr:nth-child(5) input[type=checkbox]');
  if (process.argv[3]) { await page.screenshot({ path: process.argv[3], fullPage: true }); }
  // The values DaVinci submits are what build_request parses.
  f = await fields();
  ok(f.sel === 'a2', 'submitted selection is the resource IDs');
  await browser.close();
  console.log('picker ui: ' + n + ' checks passed');
})().catch((e) => { console.error('FAILED:', e.message); process.exit(1); });
