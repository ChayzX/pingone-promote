(() => {
  'use strict';

  const $ = (selector, root = document) => root.querySelector(selector);
  const state = {
    csrf: '', regions: {}, profiles: [], history: [], sourceInventory: null, targetInventory: null,
    suggestions: {}, mappings: {}, mappingWarnings: [], selected: new Set(), expandedDependencies: new Set(),
    sourceByID: new Map(), targetByType: new Map(), searchTimer: null,
    search: '', type: '', plan: null, planMode: 'promotion', pollTimer: null, profileEditID: '',
    confirmMode: 'deploy', confirmKey: '', confirmAttempts: new Map(), profileGeneration: 0, planSource: null, planTarget: null, historyBusy: false,
    inventoryGeneration: 0, inventoryLoading: false, planGeneration: 0, planPending: false, planRequestPending: false, confirmGeneration: 0, localTargets: [], gitlabConfigGeneration: 0, gitlab: null, pendingDeleteProfile: null
  };

  const el = (tag, className, value) => {
    const node = document.createElement(tag);
    if (className) node.className = className;
    if (value !== undefined && value !== null) node.textContent = String(value);
    return node;
  };
  const text = (value, fallback = '—') => value === undefined || value === null || value === '' ? fallback : String(value);
  const stringify = value => {
    if (value === undefined) return 'Not provided';
    if (value === null) return 'null';
    if (typeof value === 'string') return value;
    try { return JSON.stringify(value, null, 2); } catch (_) { return String(value); }
  };
  const profileLabel = profile => profile ? `${text(profile.name, 'Unnamed profile')} · ${text(profile.environmentId, 'No environment ID')}` : 'Unknown environment';
  const resourceName = resource => text(resource?.name || resource?.configuration?.name || resource?.configuration?.displayName || resource?.id, 'Unnamed resource');
  const resourceType = resource => text(resource?.type || resource?.resourceType || resource?.category, 'Unknown type');
  const resourceID = resource => text(resource?.id, 'Unknown ID');
  const resourceDependencies = resource => Array.isArray(resource?.dependencies) ? resource.dependencies : [];
  const normalizedStatus = value => String(value || 'UNKNOWN').toUpperCase().replaceAll(' ', '_');
  const isTerminalStatus = value => ['READY', 'COMPLETED', 'ERROR', 'VALIDATION_FAILED', 'ROLLED_BACK'].includes(normalizedStatus(value));
  const isRollbackRecord = record => /rollback/i.test(String(record?.kind || '')) || Boolean(record?.rollbackOf || record?.previousPromotionId);
  const profilesDialog = $('#profiles-dialog');
  const deployDialog = $('#deploy-dialog');
  const gitlabDialog = $('#gitlab-dialog');
  const deleteProfileDialog = $('#delete-profile-dialog');

  async function request(path, options = {}) {
    const method = String(options.method || 'GET').toUpperCase();
    const headers = new Headers(options.headers || {});
    headers.set('Accept', 'application/json');
    if (options.body !== undefined) headers.set('Content-Type', 'application/json');
    if (method === 'POST' || method === 'DELETE') headers.set('X-CSRF-Token', state.csrf);
    let response;
    try {
      response = await fetch(path, {
        method,
        headers,
        body: options.body === undefined ? undefined : JSON.stringify(options.body),
        credentials: 'same-origin'
      });
    } catch (_) {
      throw new Error('The connection to the local app was interrupted. Check that it is running before trying again.');
    }
    let data;
    try {
      data = JSON.parse(await response.text());
      if (!data || typeof data !== 'object' || (response.ok && Object.hasOwn(data, 'error'))) throw new Error();
    } catch (_) {
      throw new Error(`The local app returned an invalid response (HTTP ${response.status}). Check that it is running and reload the app.`);
    }
    if (!response.ok) {
      const failure = new Error(typeof data.error === 'string' && data.error ? data.error : `Request failed (HTTP ${response.status}). Check the local app and retry.`);
      failure.safeToRetry = data.safeToRetry === true;
      failure.mutationNotStarted = data.mutationStarted === false;
      throw failure;
    }
    return data;
  }

  function clearTimer() {
    if (state.pollTimer) window.clearTimeout(state.pollTimer);
    state.pollTimer = null;
  }

  function showToast(message, isError = false) {
    const toast = el('div', `toast${isError ? ' toast-error' : ''}`, message);
    toast.setAttribute('role', isError ? 'alert' : 'status');
    $('#toast-region').append(toast);
    window.setTimeout(() => toast.remove(), 6000);
  }

  function setStateBox(node, { title, message, kind = 'empty', icon = 'i' }) {
    node.className = `state-box state-${kind}`;
    node.setAttribute('role', kind === 'error' ? 'alert' : 'status');
    node.setAttribute('aria-live', kind === 'error' ? 'assertive' : 'polite');
    node.setAttribute('aria-atomic', 'true');
    node.replaceChildren();
    const mark = el('span', 'state-icon', icon);
    mark.setAttribute('aria-hidden', 'true');
    const copy = el('div');
    copy.append(el('h3', '', title), el('p', '', message));
    node.append(mark, copy);
    node.hidden = false;
  }

  function profileByID(id) { return state.profiles.find(profile => String(profile.id) === String(id)); }
  function selectedProfile(which) { return profileByID($(`#${which}-profile`).value); }

  function renderProfileSelects() {
    for (const id of ['source-profile', 'target-profile']) {
      const select = $(`#${id}`);
      const selected = select.value;
      const first = document.createElement('option');
      first.value = '';
      first.textContent = 'Choose a saved profile';
      select.replaceChildren(first);
      for (const profile of state.profiles) {
        const option = document.createElement('option');
        option.value = String(profile.id);
        option.textContent = profileLabel(profile);
        select.append(option);
      }
      if (state.profiles.some(profile => String(profile.id) === selected)) select.value = selected;
    }
    updateEnvironmentMeta();
  }

  function updateEnvironmentMeta() {
    for (const which of ['source', 'target']) {
      const profile = selectedProfile(which);
      const target = $(`#${which}-meta`);
      if (!profile) target.textContent = `No ${which === 'target' ? 'destination' : 'source'} profile selected`;
      else target.textContent = `${text(profile.region, 'Region unavailable')} region · Environment ${text(profile.environmentId)}`;
    }
    const source = selectedProfile('source');
    const target = selectedProfile('target');
    const ready = Boolean(source && target && source.id !== target.id && source.environmentId !== target.environmentId);
    $('#load-resources').disabled = !ready || state.inventoryLoading;
    $('#profile-hint').textContent = state.profiles.length ? (ready ? 'Profiles are ready to load.' : 'Choose two different environment profiles.') : 'Add an environment profile to start.';
    $('#add-first-profile').hidden = state.profiles.length > 0;
  }

  function openProfiles() {
    renderProfiles();
    resetProfileForm();
    profilesDialog.showModal();
    $('#profile-editor').open = true;
    $('#profile-editor summary').focus();
  }

  function renderProfiles() {
    const list = $('#profile-list');
    const status = $('#profile-list-state');
    list.replaceChildren();
    status.className = 'inline-state';
    status.textContent = '';
    if (!state.profiles.length) {
      status.textContent = 'No saved profiles yet. Add the source and destination environments you use.';
      return;
    }
    for (const profile of state.profiles) {
      const row = el('div', 'profile-list-item');
      const main = el('div', 'profile-list-main');
      main.append(el('strong', '', text(profile.name, 'Unnamed profile')));
      main.append(el('span', '', `${text(profile.region, 'Unknown region')} · ${text(profile.environmentId, 'No environment ID')} · ${text(profile.secretMode, 'secret mode unavailable')}`));
      const actions = el('div', 'profile-list-actions');
      const edit = el('button', '', 'Edit');
      edit.type = 'button';
      edit.addEventListener('click', () => editProfile(profile));
      const remove = el('button', 'delete-profile', 'Delete');
      remove.type = 'button';
      remove.addEventListener('click', () => openDeleteProfileConfirmation(profile));
      actions.append(edit, remove);
      row.append(main, actions);
      list.append(row);
    }
  }

  function resetProfileForm() {
    state.profileGeneration += 1;
    const form = $('#profiles-form');
    form.querySelectorAll('.field input').forEach(input => { input.value = ''; });
    const region = form.querySelector('[name="region"]');
    region.value = 'us';
    const mode = form.querySelector('[name="secretMode"]');
    mode.value = 'session';
    state.profileEditID = '';
    $('#profile-editor-summary').textContent = 'Add an environment profile';
    $('#profile-save').textContent = 'Save profile';
    $('#profile-save').disabled = false;
    $('#profile-cancel-edit').hidden = true;
    $('#profile-error').hidden = true;
    $('#secret-help').textContent = 'Kept in memory for this app session.';
    updateSecretFields();
  }

  function editProfile(profile) {
    state.profileGeneration += 1;
    const form = $('#profiles-form');
    state.profileEditID = String(profile.id);
    for (const key of ['name', 'region', 'environmentId', 'authEnvironmentId', 'clientId', 'secretMode', 'secretEnv']) {
      const input = form.querySelector(`[name="${key}"]`);
      if (input) input.value = profile[key] || '';
    }
    form.querySelector('[name="secret"]').value = '';
    $('#profile-editor').open = true;
    $('#profile-editor-summary').textContent = `Edit ${text(profile.name, 'environment profile')}`;
    $('#profile-save').textContent = 'Save changes';
    $('#profile-save').disabled = false;
    $('#profile-cancel-edit').hidden = false;
    $('#profile-error').hidden = true;
    $('#secret-help').textContent = 'Leave blank to keep the stored secret. Enter a new value to replace it.';
    updateSecretFields();
    form.querySelector('[name="name"]').focus();
  }

  function updateSecretFields() {
    const mode = $('#profiles-dialog [name="secretMode"]').value;
    const envMode = mode === 'env';
    $('#profiles-dialog .secret-env-field').hidden = !envMode;
    const secretEnv = $('#profiles-dialog [name="secretEnv"]');
    secretEnv.required = envMode;
    secretEnv.disabled = !envMode;
    const secretField = $('#profiles-dialog .secret-value-field');
    secretField.hidden = envMode;
    const secret = $('#profiles-dialog [name="secret"]');
    secret.disabled = envMode;
    secret.required = !state.profileEditID && !envMode;
    if (mode === 'keychain') $('#secret-help').textContent = 'Stored in the operating system credential vault. Linux requires a Secret Service keyring to be available.';
    if (mode === 'session') $('#secret-help').textContent = state.profileEditID ? 'Leave blank to keep the stored secret; a new value replaces it.' : 'Kept in memory for this app session.';
    if (mode === 'env') $('#secret-help').textContent = '';
  }

  async function saveProfile() {
    const generation = state.profileGeneration;
    const form = $('#profiles-form');
    const error = $('#profile-error');
    error.hidden = true;
    const save = $('#profile-save');
    const editing = Boolean(state.profileEditID);
    const name = form.querySelector('[name="name"]').value;
    save.disabled = true;
    try {
      if (!form.reportValidity()) return;
      const data = Object.fromEntries(new FormData(form));
      const secret = String(data.secret || '');
      delete data.secret;
      if (state.profileEditID) data.id = state.profileEditID;
      const saved = await request('/api/profiles', { method: 'POST', body: { ...data, secret } });
      const profile = saved?.profile || saved;
      const at = state.profiles.findIndex(item => String(item.id) === String(profile.id));
      if (at >= 0) state.profiles[at] = profile;
      else state.profiles.push(profile);
      state.profiles.sort((a, b) => text(a.name).localeCompare(text(b.name)));
      renderProfileSelects();
      if (!editing && generation === state.profileGeneration) {
        $('#source-profile').value = String(profile.id);
        if (!$('#target-profile').value || $('#target-profile').value === String(profile.id)) {
          $('#target-profile').value = state.profiles.find(item => String(item.id) !== String(profile.id))?.id || '';
        }
      }
      updateEnvironmentMeta();
      invalidateInventory('Profile details changed. Load resources again to continue.');
      renderProfiles();
      if (generation === state.profileGeneration) resetProfileForm();
      showToast(`Profile “${text(profile.name, name)}” saved.`);
    } catch (failure) {
      if (generation === state.profileGeneration) {
        error.textContent = failure.message;
        error.hidden = false;
      } else showToast(`Could not save profile “${name}”: ${failure.message}`, true);
    } finally {
      if (generation === state.profileGeneration) save.disabled = false;
    }
  }

  function openDeleteProfileConfirmation(profile) {
    state.pendingDeleteProfile = profile;
    $('#delete-profile-copy').textContent = `Delete the saved profile “${text(profile.name)}”? This cannot be undone.`;
    $('#delete-profile-error').hidden = true;
    $('#confirm-delete-profile').disabled = false;
    $('#confirm-delete-profile').textContent = 'Delete';
    deleteProfileDialog.showModal();
  }

  async function performDeleteProfile() {
    const profile = state.pendingDeleteProfile;
    if (!profile) return;
    const button = $('#confirm-delete-profile');
    button.disabled = true;
    button.textContent = 'Deleting…';
    try {
      await request(`/api/profiles/${encodeURIComponent(profile.id)}`, { method: 'DELETE' });
      state.profiles = state.profiles.filter(item => String(item.id) !== String(profile.id));
      for (const selectID of ['source-profile', 'target-profile']) {
        const select = $(`#${selectID}`);
        if (select.value === String(profile.id)) select.value = '';
      }
      renderProfileSelects();
      invalidateInventory('A profile was removed. Choose profiles and load resources again.');
      renderProfiles();
      showToast('Profile deleted.');
      state.pendingDeleteProfile = null;
      deleteProfileDialog.close();
    } catch (failure) {
      $('#delete-profile-error').textContent = failure.message;
      $('#delete-profile-error').hidden = false;
      button.disabled = false;
      button.textContent = 'Delete';
    }
  }

  function invalidatePlan(message = 'Choices changed. Build a new review before deploying.') {
    state.planGeneration += 1;
    state.planPending = false;
    clearTimer();
    state.plan = null;
    state.planSource = null;
    state.planTarget = null;
    $('#plan-content').hidden = true;
    $('#deploy-bar').hidden = true;
    setStateBox($('#plan-state'), { title: 'Review needs to be rebuilt', message, icon: '↻' });
    $('#build-plan').textContent = 'Build review';
    updateBuildButton();
  }

  function invalidateInventory(message = 'Choose profiles and load resources to continue.') {
    state.inventoryGeneration += 1;
    state.inventoryLoading = false;
    $('#load-resources').textContent = 'Load resources';
    clearTimer();
    state.sourceInventory = null;
    state.targetInventory = null;
    state.suggestions = {};
    state.mappingWarnings = [];
    state.mappings = {};
    state.selected.clear();
    state.expandedDependencies.clear();
    invalidatePlan(message);
    renderResources();
    updateEnvironmentMeta();
  }

  function updateBuildButton() {
    const canBuild = Boolean(state.sourceInventory && state.targetInventory && state.selected.size > 0);
    const isGitLab = Boolean(selectedProfile('target') && !state.localTargets.includes(String(selectedProfile('target').environmentId)));
    $('#build-plan').disabled = !canBuild || state.planRequestPending;
    $('#build-plan').textContent = state.planRequestPending ? 'Building…' : isGitLab ? 'Build submission preview' : 'Build review';
    $('#selection-count').textContent = `${state.selected.size} selected`;
  }

  function filteredResources() {
    const resources = state.sourceInventory?.resources || [];
    const query = state.search.trim().toLocaleLowerCase();
    return resources.filter(resource => {
      const type = resourceType(resource);
      const matchesType = !state.type || type === state.type;
      const searchable = `${resourceName(resource)} ${type} ${resourceID(resource)}`.toLocaleLowerCase();
      return matchesType && (!query || searchable.includes(query));
    });
  }

  // Build the lookups the table needs once per inventory load, instead of
  // rebuilding a full-inventory Map on every dependency lookup and filtering
  // the whole destination list once per row. Cleared when inventories reset.
  function buildIndexes() {
    state.sourceByID = new Map((state.sourceInventory?.resources || []).map(resource => [String(resource.id), resource]));
    const byType = new Map();
    for (const target of state.targetInventory?.resources || []) {
      const type = resourceType(target);
      if (!byType.has(type)) byType.set(type, []);
      byType.get(type).push(target);
    }
    state.targetByType = byType;
  }

  function targetOptionsFor(resource) {
    return state.targetByType.get(resourceType(resource)) || [];
  }

  function dependencyResource(dep) {
    if (dep && typeof dep === 'object') return dep;
    const id = String(dep);
    return state.sourceByID.get(id) || { id, name: id, type: 'Dependency' };
  }

  function makeDependencyCell(resource) {
    const td = el('td', 'dependency-cell');
    const deps = resourceDependencies(resource);
    if (!deps.length) { td.textContent = 'None listed'; return td; }
    const reveal = el('button', 'dependency-toggle', state.expandedDependencies.has(String(resource.id)) ? 'Hide dependencies' : `Show ${deps.length} ${deps.length === 1 ? 'dependency' : 'dependencies'}`);
    reveal.type = 'button';
    reveal.setAttribute('aria-expanded', String(state.expandedDependencies.has(String(resource.id))));
    reveal.addEventListener('click', () => {
      const id = String(resource.id);
      if (state.expandedDependencies.has(id)) state.expandedDependencies.delete(id);
      else state.expandedDependencies.add(id);
      renderResources();
    });
    td.append(reveal);
    if (state.expandedDependencies.has(String(resource.id))) {
      const list = el('ul', 'dependency-list');
      deps.forEach(dep => {
        const item = dependencyResource(dep);
        const li = el('li');
        li.append(el('span', '', resourceName(item)), document.createTextNode(' · '), el('span', 'mono', resourceID(item)));
        list.append(li);
      });
      const actions = el('div', 'dependency-actions');
      const unresolved = deps.map(dependencyResource).filter(item => item.type === 'Dependency');
      const select = el('button', 'text-button', 'Select listed dependencies');
      select.type = 'button';
      select.disabled = unresolved.length > 0;
      select.title = unresolved.length ? 'Some dependencies are not present in the loaded inventory.' : '';
      select.addEventListener('click', () => {
        for (const dep of deps) {
          const depResource = dependencyResource(dep);
          const found = state.sourceInventory.resources.find(item => String(item.id) === String(depResource.id));
          if (found) state.selected.add(String(found.id));
        }
        invalidatePlan('Dependency selection changed. Build a new review.');
        renderResources();
      });
      actions.append(select);
      list.append(actions);
      td.append(list);
    }
    return td;
  }

  function mappingOption(target) {
    const option = document.createElement('option');
    option.value = String(target.id);
    option.textContent = `${resourceName(target)} · ${resourceID(target)}`;
    return option;
  }

  function mappingObjectFor(resource) {
    const targets = targetOptionsFor(resource);
    const td = el('td');
    const select = el('select', 'control mapping-select');
    select.setAttribute('aria-label', `Destination mapping for ${resourceName(resource)}`);
    const automatic = document.createElement('option');
    automatic.value = '';
    automatic.textContent = 'Automatic / create if unmatched';
    select.append(automatic);
    const mapped = String(state.mappings[String(resource.id)] ?? state.suggestions[String(resource.id)] ?? '');
    const current = mapped && targets.find(target => String(target.id) === mapped);
    // Pre-filling every row's select with the full compatible-destination list
    // (often 100+ options) dominates render time at a few hundred rows. Start
    // with just the current selection, and populate the rest on first focus.
    if (current) { select.append(mappingOption(current)); select.value = mapped; }
    else select.value = '';
    select.dataset.filled = targets.length ? 'pending' : 'done';
    const fill = () => {
      if (select.dataset.filled !== 'pending') return;
      const chosen = select.value;
      const frag = document.createDocumentFragment();
      for (const target of targets) {
        if (current && String(target.id) === mapped) continue;
        frag.append(mappingOption(target));
      }
      select.append(frag);
      select.value = chosen;
      select.dataset.filled = 'done';
    };
    select.addEventListener('pointerdown', fill);
    select.addEventListener('focus', fill);
    select.addEventListener('change', () => {
      state.mappings[String(resource.id)] = select.value;
      invalidatePlan('Destination mapping changed. Build a new review.');
    });
    const note = el('p', 'mapping-note', targets.length ? `${targets.length} compatible destination ${targets.length === 1 ? 'resource' : 'resources'} · unselected resources can still be reused.` : 'No loaded destination match of this type. Automatic mapping may create a new resource.');
    td.append(select, note);
    return td;
  }

  function renderResources() {
    const resources = state.sourceInventory?.resources || [];
    const wrap = $('#resource-table-wrap');
    const body = $('#resource-rows');
    const toolbar = $('#resource-search');
    const filter = $('#type-filter');
    const stateBox = $('#resource-state');
    body.replaceChildren();
    if (!state.sourceInventory || !state.targetInventory) {
      wrap.hidden = true;
      $('#resource-footer').hidden = true;
      $('#table-hint').hidden = true;
      $('#mapping-warnings').hidden = true;
      toolbar.disabled = true;
      filter.disabled = true;
      setStateBox(stateBox, { title: 'Resources have not been loaded', message: 'Choose a source and destination profile, then load their supported configuration resources.', icon: '↥' });
      updateBuildButton();
      return;
    }
    toolbar.disabled = false;
    filter.disabled = false;
    const typeValue = filter.value;
    const types = [...new Set(resources.map(resourceType))].sort((a, b) => a.localeCompare(b));
    filter.replaceChildren();
    const all = document.createElement('option'); all.value = ''; all.textContent = 'All types'; filter.append(all);
    for (const type of types) { const option = document.createElement('option'); option.value = type; option.textContent = type; filter.append(option); }
    filter.value = types.includes(typeValue) ? typeValue : '';
    const visible = filteredResources();
    const warningBox = $('#mapping-warnings');
    warningBox.replaceChildren();
    warningBox.hidden = !state.mappingWarnings.length;
    if (state.mappingWarnings.length) {
      warningBox.append(el('strong', '', 'Mapping suggestions need attention'));
      const warningList = el('ul');
      state.mappingWarnings.forEach(warning => warningList.append(el('li', '', typeof warning === 'string' ? warning : stringify(warning))));
      warningBox.append(warningList);
    }
    stateBox.hidden = true;
    wrap.hidden = false;
    $('#resource-footer').hidden = false;
    $('#table-hint').hidden = false;
    $('#resource-total').textContent = `${visible.length} of ${resources.length} resources`;
    if (!resources.length) {
      wrap.hidden = true;
      $('#resource-footer').hidden = true;
      $('#table-hint').hidden = true;
      setStateBox(stateBox, { title: 'No resources found', message: 'The loaded inventories did not include supported configuration resources. Refresh the inventory or choose another profile.', icon: '∅' });
    } else if (!visible.length) {
      wrap.hidden = true;
      $('#resource-footer').hidden = true;
      $('#table-hint').hidden = true;
      setStateBox(stateBox, { title: 'No matching resources', message: 'Change the search or type filter to see resources again.', icon: '⌕' });
    }
    for (const resource of visible) {
      const id = String(resource.id);
      const tr = el('tr', state.selected.has(id) ? 'is-selected' : '');
      const checkCell = el('td');
      const check = el('input', 'resource-check');
      check.type = 'checkbox'; check.checked = state.selected.has(id); check.setAttribute('aria-label', `Select ${resourceName(resource)} for promotion`);
      check.addEventListener('change', () => {
        if (check.checked) state.selected.add(id); else state.selected.delete(id);
        // Update only this row rather than rebuilding the whole table (every
        // other row's cells, dependency state and mapping select are unchanged).
        tr.classList.toggle('is-selected', check.checked);
        invalidatePlan('Resource selection changed. Build a new review.');
        updateBuildButton();
      });
      checkCell.append(check);
      const sourceCell = el('td');
      sourceCell.append(el('span', 'resource-name', resourceName(resource)), el('span', 'resource-id', resourceID(resource)));
      const typeCell = el('td'); typeCell.append(el('span', 'type-label', resourceType(resource)));
      tr.append(checkCell, sourceCell, typeCell, makeDependencyCell(resource), mappingObjectFor(resource));
      body.append(tr);
    }
    updateBuildButton();
  }

  function renderInventoryLoading() {
    $('#resource-table-wrap').hidden = true;
    $('#resource-footer').hidden = true;
    $('#table-hint').hidden = true;
    $('#mapping-warnings').hidden = true;
    $('#resource-search').disabled = true;
    $('#type-filter').disabled = true;
    setStateBox($('#resource-state'), { title: 'Loading resources', message: 'Reading both environment inventories and destination mapping suggestions.', kind: 'loading', icon: '…' });
  }

  async function loadResources() {
    const source = selectedProfile('source');
    const target = selectedProfile('target');
    if (!source || !target || source.environmentId === target.environmentId) return;
    const generation = ++state.inventoryGeneration;
    const sourceID = String(source.id);
    const targetID = String(target.id);
    state.inventoryLoading = true;
    clearTimer();
    state.sourceInventory = null; state.targetInventory = null; state.selected.clear(); state.mappings = {}; state.suggestions = {};
    state.sourceByID = new Map(); state.targetByType = new Map();
    state.mappingWarnings = [];
    invalidatePlan('Loading new inventories.');
    renderInventoryLoading();
    const button = $('#load-resources'); button.disabled = true; button.textContent = 'Loading…';
    try {
      const [sourceInventory, targetInventory] = await Promise.all([
        request('/api/inventory', { method: 'POST', body: { profileId: source.id } }).catch(failure => { throw new Error(`Source inventory: ${failure.message}`); }),
        request('/api/inventory', { method: 'POST', body: { profileId: target.id } }).catch(failure => { throw new Error(`Destination inventory: ${failure.message}`); })
      ]);
      const mappingResult = await request('/api/mappings', { method: 'POST', body: { sourceInventoryId: sourceInventory.id, targetInventoryId: targetInventory.id } }).catch(failure => { throw new Error(`Destination mapping suggestions: ${failure.message}`); });
      if (generation !== state.inventoryGeneration || String(selectedProfile('source')?.id) !== sourceID || String(selectedProfile('target')?.id) !== targetID) return;
      state.sourceInventory = sourceInventory;
      state.targetInventory = targetInventory;
      buildIndexes();
      state.suggestions = mappingResult?.mappings || {};
      state.mappings = { ...state.suggestions };
      state.mappingWarnings = Array.isArray(mappingResult?.warnings) ? mappingResult.warnings : [];
      state.search = ''; state.type = '';
      $('#resource-search').value = '';
      invalidatePlan('Resources loaded. Select resources and build a review to continue.');
      renderResources();
      const warnings = state.mappingWarnings;
      if (warnings.length) showToast(`${sourceInventory.resources?.length || 0} source resources loaded. ${warnings.length} mapping warning${warnings.length === 1 ? '' : 's'} need review.`);
      else showToast(`${sourceInventory.resources?.length || 0} source resources loaded.`);
    } catch (failure) {
      if (generation === state.inventoryGeneration) {
        setStateBox($('#resource-state'), { title: 'Could not load resources', message: failure.message, kind: 'error', icon: '!' });
        invalidatePlan('Load both inventories successfully before building a review. See the resource error above.');
      }
    } finally {
      if (generation === state.inventoryGeneration) {
        state.inventoryLoading = false;
        button.textContent = 'Load resources';
        updateEnvironmentMeta();
      }
    }
  }

  function updateFilterValues() {
    state.type = $('#type-filter').value;
    renderResources();
  }

  // Debounce search: re-rendering the table on every keystroke is wasted work
  // while the user is still typing. The type filter stays immediate.
  function onSearchInput() {
    state.search = $('#resource-search').value;
    if (state.searchTimer) clearTimeout(state.searchTimer);
    state.searchTimer = setTimeout(() => { state.searchTimer = null; renderResources(); }, 150);
  }

  function safeArray(value) { return Array.isArray(value) ? value : value == null ? [] : [value]; }
  function appendValue(container, label, value) {
    const block = el('div', 'change-value');
    block.append(el('span', 'change-label', label), el('pre', '', stringify(value)));
    container.append(block);
  }

  function renderSteps(plan) {
    const review = plan?.review || {};
    const steps = safeArray(review.steps);
    const section = el('section', 'steps-section');
    const changeSummary = review.summary || {};
    const summary = el('div', 'change-summary');
    summary.append(el('strong', '', 'Change summary'));
    summary.append(el('p', '', `${Number(changeSummary.create) || 0} create · ${Number(changeSummary.change) || 0} change · ${Number(changeSummary.destroy) || 0} destroy · ${Number(changeSummary.unsupported) || 0} unsupported`));
    summary.append(el('small', '', 'Counts are classified locally from PingOne’s native operation methods and paths.'));
    section.append(summary);
    const heading = el('div', 'steps-heading');
    heading.append(el('h3', '', 'Operations'), el('span', 'step-count', `${steps.length} ${steps.length === 1 ? 'operation' : 'operations'}`));
    section.append(heading);
    if (!steps.length) {
      section.append(el('div', 'inline-state', 'No operation details were returned by the plan. Deployment remains blocked until a complete review is available.'));
      return section;
    }
    const list = el('div', 'step-list');
    for (const step of steps) {
      const card = el('article', 'step-card');
      const head = el('div', 'step-head');
      const title = el('div', 'step-title');
      title.append(el('strong', '', text(step.name || step.sourceName || step.targetName, 'Configuration change')));
      title.append(el('span', '', `Source ${text(step.sourceId)} → destination ${text(step.targetId, 'new resource')}`));
      head.append(title, el('span', 'action-pill', text(step.action, 'UNKNOWN')));
      card.append(head);
      const body = el('div', 'step-body');
      const ids = el('div', 'step-identities');
      const source = el('div', 'step-identity'); source.append(el('span', '', 'Source'), el('strong', '', text(step.sourceId)), el('span', 'mono step-url', text(step.sourceUrl, '')));
      const target = el('div', 'step-identity'); target.append(el('span', '', 'Destination'), el('strong', '', text(step.targetId, 'New resource')), el('span', 'mono step-url', text(step.url, '')));
      ids.append(source, target);
      body.append(ids);
      const changes = safeArray(step.changes);
      if (changes.length) {
        const changeList = el('div', 'changes-list');
        for (const change of changes) {
          const row = el('div', 'change-row');
          if (typeof change === 'string') {
            row.append(el('div', 'change-path', change));
            changeList.append(row);
            continue;
          }
          row.append(el('div', 'change-path', text(change.path || change.field || change.name, 'Change')));
          if ('before' in change || 'after' in change) {
            const values = el('div', 'change-values');
            appendValue(values, 'Before', change.before);
            appendValue(values, 'After', change.after);
            row.append(values);
          } else row.append(el('div', 'change-path', stringify(change)));
          changeList.append(row);
        }
        body.append(changeList);
      } else {
        body.append(el('p', 'mapping-note', 'Field-level change paths were not returned for this operation.'));
      }
      const diff = el('details', 'step-diff');
      diff.append(el('summary', '', 'View before and after values'));
      const values = el('div', 'change-values');
      appendValue(values, 'Before', step.before ?? 'No existing target configuration returned');
      appendValue(values, 'After', step.after ?? 'No resulting configuration returned');
      diff.append(values);
      body.append(diff);
      if (step.issue) body.append(el('p', 'step-issue', stringify(step.issue)));
      card.append(body);
      list.append(card);
    }
    section.append(list);
    return section;
  }

  function planErrors(plan) {
    const review = plan?.review || {};
    const issues = [...safeArray(review.issues), ...safeArray(plan?.errors)];
    return issues.map(issue => typeof issue === 'string' ? issue : stringify(issue));
  }

  function renderPlan(plan, generation = state.planGeneration) {
    if (generation !== state.planGeneration) return;
    state.plan = plan;
    $('#plan-state').hidden = true;
    const content = $('#plan-content'); content.replaceChildren(); content.hidden = false;
    if (plan.gitlabRequired || plan.request) {
      state.plan.gitlabRequired = true;
      renderGitLabPreview(plan, content);
      $('#deploy-bar').hidden = true;
      clearTimer();
      return;
    }
    const overview = el('div', 'plan-overview');
    const summary = el('div', 'plan-summary');
    const status = normalizedStatus(plan.status);
    const statusClass = ['READY', 'COMPLETED', 'ROLLED_BACK'].includes(status) ? status.toLowerCase() : ['ERROR', 'VALIDATION_FAILED'].includes(status) ? status.toLowerCase() : ['IN_PROGRESS', 'PREPARING', 'ROLLBACK_NEW', 'ROLLBACK_PREPARING', 'ROLLBACK_IN_PROGRESS'].includes(status) ? 'pending' : 'neutral';
    const summaryHead = el('div');
    summaryHead.append(el('h3', '', text(plan.description || plan.intent, state.planMode === 'rollback' ? 'Rollback review' : 'Promotion review')));
    summaryHead.append(el('span', `status-pill status-${statusClass}`, status.replaceAll('_', ' ')));
    summary.append(summaryHead);
    summary.append(el('p', '', `Plan ${text(plan.id)}${plan.nativeId ? ` · PingOne ID ${plan.nativeId}` : ''}`));
    if (state.planSource || state.planTarget) summary.append(el('p', '', `From ${profileLabel(state.planSource)} to ${profileLabel(state.planTarget)}`));
    if (plan.review?.digest) summary.append(el('p', 'mono', `Review digest ${plan.review.digest}`));
    const issueBox = el('div', 'plan-issues');
    const errors = planErrors(plan);
    issueBox.append(el('h3', '', errors.length ? 'Issues to resolve' : 'Review checks'));
    if (errors.length) {
      const ul = el('ul', 'issues-list');
      errors.forEach(issue => ul.append(el('li', '', issue)));
      issueBox.append(ul);
    } else issueBox.append(el('p', 'section-help', plan.canDeploy ? 'No blocking issues were returned.' : 'Deployment is unavailable until PingOne reports a ready plan.'));
    overview.append(summary, issueBox);
    content.append(overview, renderSteps(plan));
    const ready = Boolean(plan.canDeploy && plan.review?.digest && normalizedStatus(plan.status) === 'READY' && !errors.length);
    const complete = ['COMPLETED', 'ROLLED_BACK'].includes(status);
    $('#deploy-bar').hidden = false;
    $('#deploy-button').disabled = !ready;
    $('#deploy-button').textContent = complete ? (state.planMode === 'rollback' ? 'Rollback complete' : 'Promotion complete') : state.planMode === 'rollback' ? 'Execute rollback' : 'Deploy';
    $('#deploy-state-label').textContent = complete ? (status === 'ROLLED_BACK' ? 'Promotion rolled back' : state.planMode === 'rollback' ? 'Rollback completed' : 'Promotion completed') : ready ? 'Review is ready' : 'Deployment is blocked';
    $('#deploy-reason').textContent = complete ? 'This operation has finished. Check promotion history for its final status.' : ready ? 'The current review is ready for explicit confirmation.' : errors.length ? 'Resolve the listed issues, then build a new review.' : `PingOne plan status is ${status.replaceAll('_', ' ')}. Refresh or resolve the status before deployment.`;
    schedulePoll(plan, generation);
  }

  function renderGitLabPreview(plan, content) {
    const req = plan.request || plan;
    const box = el('div', 'gitlab-preview');
    box.append(el('div', 'warning-box', 'Submission preview: final PingOne plan and deployment approval happen in GitLab. No production changes are made from this browser.'));
    const meta = el('section', 'preview-section');
    meta.append(el('h3', '', 'Pinned request'));
    meta.append(el('p', 'mono', `Request ${text(req.id)} · Created ${text(req.createdAt)}`));
    meta.append(el('p', '', `Source environment ${text(req.source?.id)} · ${text(req.source?.region)} · organization ${text(req.source?.organizationId)} · snapshot ${text(req.source?.snapshotId)}`));
    meta.append(el('p', '', `Target environment ${text(req.target?.id)} · ${text(req.target?.region)} · organization ${text(req.target?.organizationId)} · snapshot ${text(req.target?.snapshotId)}`));
    box.append(meta);
    const selected = el('section', 'preview-section'); selected.append(el('h3', '', 'Selected resources and mappings'));
    const list = el('ul', 'issues-list');
    const mappings = req.mappings || {};
    for (const id of safeArray(req.selected)) list.append(el('li', '', `${id} → ${text(mappings[id], 'automatic / create')}`));
    selected.append(list); box.append(selected);
    const exports = req.configurations || {};
    const configuration = el('details', 'preview-section'); configuration.append(el('summary', '', 'Redacted configuration exports'));
    configuration.append(el('pre', 'preview-json', stringify(exports))); box.append(configuration);
    const fingerprints = el('details', 'preview-section'); fingerprints.append(el('summary', '', 'Configuration fingerprints'));
    fingerprints.append(el('pre', 'preview-json', stringify(req.fingerprints || {}))); box.append(fingerprints);
    const issues = safeArray(plan.issues);
    if (issues.length) { const ul = el('ul', 'issues-list'); issues.forEach(issue => ul.append(el('li', '', stringify(issue)))); box.append(ul); }
    const actions = el('div', 'preview-actions');
    const submit = el('button', 'button button-primary', 'Submit to GitLab'); submit.type = 'button';
    submit.disabled = !state.gitlab?.configured || !state.gitlab?.tokenAvailable;
    submit.title = submit.disabled ? 'Configure GitLab in GitLab settings before submitting.' : '';
    submit.addEventListener('click', () => openGitLabConfirmation(req));
    actions.append(submit);
    if (submit.disabled) actions.append(el('span', 'section-help', 'Configure GitLab settings before submitting this request.'));
    box.append(actions); content.append(box);
  }

  function openGitLabConfirmation(req) {
    state.reviewedRequestId = String(req.id || '');
    state.confirmGeneration = ++state.planGeneration;
    state.confirmMode = 'gitlab-submit';
    state.confirmKey = `gitlab-submit:${state.reviewedRequestId}`;
    $('#confirm-eyebrow').textContent = 'GitLab submission';
    $('#deploy-dialog-title').textContent = 'Submit this request to GitLab?';
    $('#confirm-target-name').textContent = profileLabel(state.planTarget);
    $('#deploy-warning').hidden = false;
    $('#deploy-warning').textContent = 'This creates a branch and merge request in the configured private GitLab project. Production planning, approval, deployment and rollback happen through your protected GitLab runner.';
    $('#deploy-confirm-copy').textContent = `Submit reviewed request ${text(req.id)} with its pinned source snapshot, target environment, resource selection, mappings, redacted exports and fingerprints.`;
    $('#gitlab-submit-options').hidden = false;
    $('#gitlab-branch-name').value = '';
    $('#gitlab-branch-name').placeholder = `promotion/${text(req.id, '')}`;
    $('#gitlab-commit-message').value = '';
    $('#gitlab-commit-message').placeholder = `Add promotion request ${text(req.id, '')}`;
    $('#deploy-confirm-check').checked = false;
    $('#confirm-deploy').disabled = true;
    $('#confirm-deploy').textContent = 'Submit to GitLab';
    $('#deploy-error').hidden = true;
    restoreConfirmationAttempt();
    deployDialog.showModal();
  }

  async function openGitLabSettings() {
    const generation = ++state.gitlabConfigGeneration;
    const form = $('#gitlab-form');
    $('#gitlab-error').hidden = true;
    try {
      const result = await request('/api/gitlab');
      if (generation !== state.gitlabConfigGeneration) return;
      state.gitlab = result;
      const config = result.config || {};
      for (const key of ['baseUrl', 'projectId', 'tokenEnv', 'environment']) form.elements[key].value = config[key] || (key === 'tokenEnv' ? 'GITLAB_TOKEN' : key === 'environment' ? 'production' : '');
      form.elements.token.value = '';
      $('#gitlab-config-state').textContent = result.configured ? `Configured · private project verified · token ${result.tokenAvailable ? 'available' : 'unavailable'}` : 'GitLab is not configured.';
      gitlabDialog.showModal();
    } catch (failure) { if (generation === state.gitlabConfigGeneration) { $('#gitlab-error').textContent = failure.message; $('#gitlab-error').hidden = false; gitlabDialog.showModal(); } }
  }

  async function saveGitLabSettings() {
    const generation = ++state.gitlabConfigGeneration;
    const form = $('#gitlab-form');
    if (!form.reportValidity()) return;
    const body = Object.fromEntries(new FormData(form));
    $('#gitlab-save').disabled = true; $('#gitlab-error').hidden = true;
    try {
      const result = await request('/api/gitlab', { method: 'POST', body });
      if (generation !== state.gitlabConfigGeneration) return;
      state.gitlab = result;
      form.elements.token.value = '';
      $('#gitlab-config-state').textContent = `Configured · ${text(result.project?.pathWithNamespace || result.project?.name, 'private project verified')} · token ${result.tokenAvailable ? 'available' : 'unavailable'}`;
      showToast('GitLab settings saved and project verified.');
      invalidatePlan('GitLab settings changed. Rebuild the request preview before submitting.');
    } catch (failure) { if (generation === state.gitlabConfigGeneration) { $('#gitlab-error').textContent = failure.message; $('#gitlab-error').hidden = false; } }
    finally { if (generation === state.gitlabConfigGeneration) $('#gitlab-save').disabled = false; }
  }

  function schedulePoll(plan, generation = state.planGeneration) {
    clearTimer();
    if (generation !== state.planGeneration || !plan?.id || isTerminalStatus(plan.status)) return;
    const planID = String(plan.id);
    state.pollTimer = window.setTimeout(async () => {
      if (generation !== state.planGeneration || !state.plan || String(state.plan.id) !== planID) return;
      try {
        const updated = await request(`/api/plans/${encodeURIComponent(planID)}`);
        if (generation !== state.planGeneration || !state.plan || String(state.plan.id) !== planID) return;
        renderPlan(updated, generation);
      } catch (failure) {
        if (generation !== state.planGeneration || !state.plan || String(state.plan.id) !== planID) return;
        showToast(`Could not refresh plan status: ${failure.message}`, true);
        state.pollTimer = window.setTimeout(() => schedulePoll(state.plan, generation), 2000);
      }
    }, 2000);
  }

  function makePlanBody() {
    const mappings = {};
    for (const resource of state.sourceInventory?.resources || []) {
      const id = String(resource.id);
      const mapped = Object.hasOwn(state.mappings, id) ? state.mappings[id] : state.suggestions[id];
      if (mapped) mappings[id] = mapped;
    }
    return {
      sourceInventoryId: state.sourceInventory.id,
      targetInventoryId: state.targetInventory.id,
      selected: [...state.selected],
      mappings,
      description: $('#plan-description').value.trim() || `Promotion from ${text(selectedProfile('source')?.name)} to ${text(selectedProfile('target')?.name)}`
    };
  }

  async function buildPlan() {
    if (state.planRequestPending || !state.sourceInventory || !state.targetInventory || !state.selected.size) return;
    const generation = ++state.planGeneration;
    state.planPending = true;
    state.planRequestPending = true;
    const source = selectedProfile('source');
    const target = selectedProfile('target');
    const sourceInventoryID = String(state.sourceInventory.id);
    const targetInventoryID = String(state.targetInventory.id);
    const body = makePlanBody();
    clearTimer();
    const button = $('#build-plan'); button.disabled = true; button.textContent = 'Building…';
    setStateBox($('#plan-state'), { title: 'Building review', message: 'Creating a native plan and collecting its complete operation details.', kind: 'loading', icon: '…' });
    $('#plan-content').hidden = true; $('#deploy-bar').hidden = true;
    try {
      const gitlabRequired = !state.localTargets.includes(String(target?.environmentId || ''));
      const plan = await request(gitlabRequired ? '/api/requests/preview' : '/api/plans', { method: 'POST', body });
      if (generation !== state.planGeneration || String(state.sourceInventory?.id) !== sourceInventoryID || String(state.targetInventory?.id) !== targetInventoryID || String(selectedProfile('source')?.id) !== String(source?.id) || String(selectedProfile('target')?.id) !== String(target?.id)) return;
      state.planMode = 'promotion';
      state.planSource = source;
      state.planTarget = target;
      if (plan.request || plan.gitlabRequired) plan.gitlabRequired = true;
      renderPlan(plan, generation);
    } catch (failure) {
      if (generation === state.planGeneration) setStateBox($('#plan-state'), { title: 'Could not build review', message: failure.message, kind: 'error', icon: '!' });
      else {
        const message = `Earlier request for ${profileLabel(source)} → ${profileLabel(target)}: ${failure.message}`;
        if (!state.plan && !$('#plan-state').classList.contains('state-loading')) setStateBox($('#plan-state'), { title: 'Previous review request failed', message, kind: 'error', icon: '!' });
        else showToast(message, true);
      }
    } finally {
      state.planRequestPending = false;
      if (generation === state.planGeneration) {
        state.planPending = false;
        button.textContent = 'Build review';
      }
      updateBuildButton();
    }
  }

  async function reviewHistory(record) {
    const generation = ++state.planGeneration;
    clearTimer();
    state.plan = null;
    state.planSource = null;
    state.planTarget = null;
    $('#plan-content').hidden = true;
    $('#deploy-bar').hidden = true;
    setStateBox($('#plan-state'), { title: 'Loading promotion review', message: 'Fetching the saved plan and current PingOne status.', kind: 'loading', icon: '…' });
    const button = document.querySelector(`[data-review-id="${CSS.escape(String(record.id))}"]`);
    if (button) { button.disabled = true; button.textContent = 'Loading…'; }
    try {
      const plan = await request(`/api/plans/${encodeURIComponent(record.id)}`);
      if (generation !== state.planGeneration) return;
      state.planMode = isRollbackRecord(record) ? 'rollback' : 'promotion';
      state.planSource = record.source || null;
      state.planTarget = record.target || null;
      renderPlan(plan, generation);
      $('#review-title').scrollIntoView({ behavior: 'smooth', block: 'start' });
    } catch (failure) { if (generation === state.planGeneration) setStateBox($('#plan-state'), { title: 'Could not review promotion', message: failure.message, kind: 'error', icon: '!' }); }
    finally { if (button) { button.disabled = false; button.textContent = 'Review'; } }
  }

  function openDeployConfirmation(mode) {
    state.confirmGeneration = ++state.planGeneration;
    clearTimer();
    state.confirmMode = mode;
    $('#gitlab-submit-options').hidden = true;
    state.confirmKey = `${mode}:${mode === 'rollback-draft' ? state.rollbackRecord?.id : state.plan?.id}`;
    const rollbackDraft = mode === 'rollback-draft';
    const rollbackExecute = mode === 'rollback-execute';
    const target = rollbackDraft ? state.rollbackRecord?.target : state.planTarget || selectedProfile('target');
    $('#confirm-eyebrow').textContent = rollbackDraft || rollbackExecute ? 'Rollback check' : 'Final check';
    $('#deploy-dialog-title').textContent = rollbackDraft ? 'Create a rollback review?' : rollbackExecute ? 'Execute this rollback?' : 'Deploy this promotion?';
    $('#confirm-target-name').textContent = profileLabel(target);
    $('#deploy-warning').hidden = !rollbackDraft;
    $('#deploy-warning').textContent = rollbackDraft ? 'Rollback is available only for the latest promotion into this destination. It restores the whole promotion and can overwrite manual changes made since deployment. PingOne will confirm eligibility.' : '';
    $('#deploy-confirm-copy').textContent = rollbackDraft ? 'This creates a separate rollback draft. No configuration changes happen until you review its operations and explicitly execute the rollback.' : rollbackExecute ? 'Review the rollback operations and destination below. Executing applies the complete rollback promotion.' : 'Review the native operations and destination below. Deploy sends this reviewed plan to PingOne.';
    $('#deploy-confirm-check').checked = false;
    $('#confirm-deploy').disabled = true;
    $('#confirm-deploy').textContent = rollbackDraft ? 'Create rollback review' : rollbackExecute ? 'Execute rollback' : 'Deploy';
    $('#deploy-error').hidden = true;
    restoreConfirmationAttempt();
    deployDialog.showModal();
  }

  function restoreConfirmationAttempt() {
    const attempt = state.confirmAttempts.get(state.confirmKey);
    if (!attempt) return;
    $('#deploy-error').textContent = attempt.message;
    $('#deploy-error').hidden = false;
    $('#confirm-deploy').disabled = true;
    $('#confirm-deploy').textContent = attempt.label || (state.confirmMode === 'gitlab-submit' ? 'Check GitLab' : 'Check history');
  }

  function recordConfirmationAttempt(key, message, label) {
    state.confirmAttempts.set(key, { message, label });
    if (key === state.confirmKey && deployDialog.open) restoreConfirmationAttempt();
  }

  async function confirmDeployAction() {
    const generation = state.confirmGeneration;
    const key = state.confirmKey;
    const mode = state.confirmMode;
    if (generation !== state.planGeneration || !$('#deploy-confirm-check').checked || state.confirmAttempts.has(key)) return;
    state.confirmAttempts.set(key, { message: 'This request is in progress. Wait for its result before taking another action.', label: 'Request in progress' });
    let requestStarted = false;
    const button = $('#confirm-deploy');
    button.disabled = true;
    button.textContent = state.confirmMode === 'rollback-draft' ? 'Creating review…' : 'Submitting…';
    $('#deploy-error').hidden = true;
    try {
      if (state.confirmMode === 'gitlab-submit') {
        const requestId = state.reviewedRequestId;
        if (!requestId || !state.plan?.gitlabRequired) throw new Error('The reviewed GitLab request is missing. Build a fresh submission preview.');
        requestStarted = true;
        const submission = { requestId };
        const branchName = $('#gitlab-branch-name').value;
        const commitMessage = $('#gitlab-commit-message').value;
        if (branchName !== '') submission.branchName = branchName;
        if (commitMessage !== '') submission.commitMessage = commitMessage;
        const result = await request('/api/gitlab/submit', { method: 'POST', body: submission });
        recordConfirmationAttempt(key, 'This request was submitted. Check GitLab for its branch and merge request.');
        if (generation !== state.planGeneration || requestId !== state.reviewedRequestId) return;
        deployDialog.close();
        const toast = el('div', 'toast'); toast.setAttribute('role', 'status'); toast.append(document.createTextNode('Merge request created: '));
        const link = document.createElement('a'); link.textContent = `${text(result.branch)} !${text(result.iid)}`;
        try { const url = new URL(result.url); if (url.protocol === 'https:' && url.origin === new URL(state.gitlab?.config?.baseUrl || '').origin) { link.href = url.href; link.target = '_blank'; link.rel = 'noopener'; } else throw new Error(); } catch (_) { toast.textContent = 'GitLab created a merge request, but returned an unsafe or unverified URL.'; }
        if (link.href) toast.append(link); $('#toast-region').append(toast); window.setTimeout(() => toast.remove(), 12000);
      } else if (state.confirmMode === 'rollback-draft') {
        const record = state.rollbackRecord;
        requestStarted = true;
        const rollback = await request(`/api/plans/${encodeURIComponent(record.id)}/rollback`, { method: 'POST', body: { confirm: true } });
        recordConfirmationAttempt(key, 'This rollback draft was submitted. Check promotion history before making another request.');
        if (generation !== state.planGeneration) return;
        state.planMode = 'rollback';
        state.planSource = record.source || null;
        state.planTarget = record.target || null;
        renderPlan(rollback, generation);
        deployDialog.close();
        $('#review-title').scrollIntoView({ behavior: 'smooth', block: 'start' });
        showToast('Rollback review created. Inspect every operation before executing it.');
        await refreshHistory(false);
      } else {
        const plan = state.plan;
        if (!plan?.id || !plan.review?.digest) throw new Error('The reviewed plan or digest is missing. Build a fresh review before continuing.');
        requestStarted = true;
        const result = await request(`/api/plans/${encodeURIComponent(plan.id)}/deploy`, { method: 'POST', body: { digest: plan.review.digest, confirm: true } });
        recordConfirmationAttempt(key, 'This execution was submitted. Check promotion history for its current status.');
        if (generation !== state.planGeneration) return;
        state.planMode = state.confirmMode === 'rollback-execute' ? 'rollback' : 'promotion';
        renderPlan(result, generation);
        deployDialog.close();
        showToast(state.planMode === 'rollback' ? 'Rollback execution submitted. Status will refresh automatically.' : 'Deployment submitted. Status will refresh automatically.');
        await refreshHistory(false);
      }
    } catch (failure) {
      if (failure.safeToRetry === true) {
        state.confirmAttempts.delete(key);
        if (key === state.confirmKey && deployDialog.open) {
          $('#deploy-error').textContent = failure.message;
          $('#deploy-error').hidden = false;
          button.textContent = mode === 'gitlab-submit' ? 'Retry submission' : 'Retry request';
          button.disabled = !$('#deploy-confirm-check').checked || state.confirmGeneration !== state.planGeneration;
        }
        return;
      }
      const notStarted = !requestStarted || failure.mutationNotStarted;
      const message = notStarted ? failure.message : mode === 'gitlab-submit' ? `${failure.message} Check GitLab for the request before submitting again.` : `${failure.message} Check promotion history before making another request; PingOne may have received it.`;
      recordConfirmationAttempt(key, message, notStarted ? 'Review required' : undefined);
      if (!notStarted && mode !== 'gitlab-submit' && mode !== 'rollback-draft' && generation === state.planGeneration) {
        $('#deploy-button').disabled = true;
        try {
          const current = state.plan?.id ? await request(`/api/plans/${encodeURIComponent(state.plan.id)}`) : null;
          if (current && generation === state.planGeneration) renderPlan(current, generation);
        } catch (_) { /* Keep the deployment disabled until the user reconciles history. */ }
      }
    }
  }

  function recordRequiresGitLab(record) {
    return Boolean(record?.gitlabRequired || !state.localTargets.includes(String(record?.target?.environmentId || '')));
  }

  function canOfferRollback(record) {
    if (recordRequiresGitLab(record)) return false;
    const status = normalizedStatus(record.status);
    return !isRollbackRecord(record) && ['COMPLETED', 'ERROR'].includes(status);
  }

  function makeHistoryActions(record) {
    const actions = el('div', 'history-actions');
    const review = el('button', 'history-action', 'Review');
    review.type = 'button'; review.dataset.reviewId = String(record.id);
    review.addEventListener('click', () => reviewHistory(record));
    actions.append(review);
    if (recordRequiresGitLab(record)) {
      actions.append(el('span', 'history-sub', 'Use the GitLab pipeline for production plan, deployment, and rollback.'));
      return actions;
    }
    const rollback = el('button', 'history-action', 'Rollback');
    rollback.type = 'button'; rollback.disabled = !canOfferRollback(record);
    rollback.title = rollback.disabled ? (isRollbackRecord(record) ? 'Rollback records cannot be rolled back again.' : 'Only completed or failed promotions can be offered for rollback; PingOne checks latest-only eligibility.') : 'Create a reviewable rollback draft.';
    rollback.addEventListener('click', () => {
      state.rollbackRecord = record;
      openDeployConfirmation('rollback-draft');
    });
    actions.append(rollback);
    return actions;
  }

  function historyStatus(status) {
    const value = normalizedStatus(status);
    const klass = ['COMPLETED', 'READY', 'ROLLED_BACK'].includes(value) ? value.toLowerCase() : ['ERROR', 'VALIDATION_FAILED'].includes(value) ? 'error' : ['IN_PROGRESS', 'PREPARING', 'ROLLBACK_NEW', 'ROLLBACK_PREPARING', 'ROLLBACK_IN_PROGRESS'].includes(value) ? 'pending' : 'neutral';
    return el('span', `status-pill status-${klass}`, value.replaceAll('_', ' '));
  }

  function renderHistory() {
    const rows = $('#history-rows'); rows.replaceChildren();
    const cards = $('#history-cards'); cards.replaceChildren();
    const box = $('#history-state');
    if (!state.history.length) {
      $('#history-table-wrap').hidden = true; cards.hidden = true;
      setStateBox(box, { title: 'No promotion history', message: 'Completed and in-progress promotion records will appear here.', icon: '◷' });
      return;
    }
    box.hidden = true;
    $('#history-table-wrap').hidden = false;
    cards.hidden = false;
    for (const record of state.history) {
      const tr = el('tr');
      const title = el('td'); title.append(el('span', 'history-main', text(record.description || record.intent, record.kind || 'Promotion')), el('span', 'history-sub', `Record ${text(record.id)}${record.nativeId ? ` · PingOne ${record.nativeId}` : ''}`));
      const source = el('td'); source.append(el('span', 'history-main', text(record.source?.name, 'Source unavailable')), el('span', 'history-sub', text(record.source?.environmentId)));
      const target = el('td'); target.append(el('span', 'history-main', text(record.target?.name, 'Destination unavailable')), el('span', 'history-sub', text(record.target?.environmentId)));
      const status = el('td'); status.append(historyStatus(record.status));
      const date = el('td'); date.textContent = record.createdAt ? new Date(record.createdAt).toLocaleString() : '—';
      const actionCell = el('td'); actionCell.append(makeHistoryActions(record));
      tr.append(title, source, target, status, date, actionCell); rows.append(tr);

      const card = el('article', 'history-card');
      const cardHead = el('div', 'history-card-head');
      cardHead.append(el('strong', 'history-main', text(record.description || record.intent, record.kind || 'Promotion')), historyStatus(record.status));
      card.append(cardHead);
      const dl = el('dl');
      for (const [label, value] of [['Source', profileLabel(record.source)], ['Destination', profileLabel(record.target)], ['Created', record.createdAt ? new Date(record.createdAt).toLocaleString() : '—'], ['Record', record.id]]) {
        dl.append(el('dt', '', label), el('dd', '', value));
      }
      card.append(dl, makeHistoryActions(record)); cards.append(card);
    }
  }

  async function refreshHistory(showMessage = true) {
    if (state.historyBusy) return;
    state.historyBusy = true;
    const button = $('#refresh-history');
    button.disabled = true;
    try {
      const result = await request('/api/history');
      state.history = Array.isArray(result) ? result : result?.records || [];
      renderHistory();
      if (showMessage) showToast('History refreshed.');
    } catch (failure) {
      setStateBox($('#history-state'), { title: 'Could not load history', message: failure.message, kind: 'error', icon: '!' });
    } finally {
      state.historyBusy = false; button.disabled = false;
    }
  }

  async function initialize() {
    try {
      const session = await request('/api/session');
      state.csrf = session.csrf || '';
      state.localTargets = safeArray(session.localTargets).map(String);
      state.regions = session.regions || {};
      const [profiles, history] = await Promise.all([request('/api/profiles'), request('/api/history')]);
      state.profiles = Array.isArray(profiles) ? profiles : profiles?.profiles || [];
      state.history = Array.isArray(history) ? history : history?.records || [];
      renderProfileSelects(); renderProfiles(); renderHistory();
      try { state.gitlab = await request('/api/gitlab'); } catch (_) { state.gitlab = null; }
      if (!state.csrf) showToast('The local session did not provide a CSRF token. Reload the app before saving or deploying.', true);
    } catch (failure) {
      renderProfileSelects(); renderHistory();
      setStateBox($('#history-state'), { title: 'Local app is unavailable', message: failure.message, kind: 'error', icon: '!' });
      $('#profile-hint').textContent = 'The local app could not load its session. Reload after the server is available.';
      $('#profiles-open').disabled = true;
      showToast(`Could not initialize the local app: ${failure.message}`, true);
    }
  }

  $('#profiles-open').addEventListener('click', openProfiles);
  $('#gitlab-open').addEventListener('click', openGitLabSettings);
  $('#gitlab-save').addEventListener('click', saveGitLabSettings);
  $('#add-first-profile').addEventListener('click', openProfiles);
  // Enter inside a field must save (never close the dialog and drop the input).
  $('#profiles-form').addEventListener('submit', event => { event.preventDefault(); saveProfile(); });
  $('#profiles-close').addEventListener('click', () => profilesDialog.close());
  $('#profile-cancel-edit').addEventListener('click', resetProfileForm);
  $('#profiles-dialog [name="secretMode"]').addEventListener('change', updateSecretFields);
  $('#deploy-confirm-check').addEventListener('change', event => { $('#confirm-deploy').disabled = !event.target.checked || state.confirmAttempts.has(state.confirmKey) || state.confirmGeneration !== state.planGeneration; });
  $('#confirm-deploy').addEventListener('click', confirmDeployAction);
  $('#load-resources').addEventListener('click', loadResources);
  $('#resource-search').addEventListener('input', onSearchInput);
  $('#type-filter').addEventListener('change', updateFilterValues);
  $('#build-plan').addEventListener('click', buildPlan);
  $('#plan-description').addEventListener('input', () => { if (state.plan || state.planPending) invalidatePlan('Promotion description changed. Build a new review before continuing.'); });
  $('#deploy-button').addEventListener('click', () => openDeployConfirmation(state.planMode === 'rollback' ? 'rollback-execute' : 'deploy'));
  $('#refresh-history').addEventListener('click', () => refreshHistory());
  $('#clear-selection').addEventListener('click', () => {
    state.selected.clear(); invalidatePlan('Resource selection changed. Build a new review.'); renderResources();
  });
  for (const select of document.querySelectorAll('.profile-select')) {
    select.addEventListener('change', () => {
      updateEnvironmentMeta();
      invalidateInventory('Environment selection changed. Load resources again to continue.');
    });
  }
  deployDialog.addEventListener('close', () => { $('#deploy-confirm-check').checked = false; });
  profilesDialog.addEventListener('close', () => { resetProfileForm(); });
  $('#confirm-delete-profile').addEventListener('click', performDeleteProfile);
  deleteProfileDialog.addEventListener('close', () => { state.pendingDeleteProfile = null; });
  initialize();
})();
