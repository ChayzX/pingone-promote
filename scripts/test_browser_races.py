"""Race regressions for the PingOne Promote browser UI.
Run against the local test fixture whose URL is stored in browser-fixture.json.
"""
import asyncio
import json
from pathlib import Path
from playwright.async_api import async_playwright, expect

import argparse,ipaddress,urllib.parse
parser=argparse.ArgumentParser(description='Check local fixture UI stale-response and mapping regressions.')
parser.add_argument('--fixture-file',required=True)
parser.add_argument('--chromium',default='/usr/bin/chromium')
args=parser.parse_args()
FIXTURE=Path(args.fixture_file)

async def start_page(browser, fixture):
    page = await browser.new_page()
    await page.goto(fixture['url'])
    await page.locator('#source-profile option').nth(1).wait_for(state='attached')
    await page.locator('#source-profile').select_option(fixture['source'])
    await page.locator('#target-profile').select_option(fixture['target'])
    return page

async def loaded_page(browser, fixture):
    page = await start_page(browser, fixture)
    await page.locator('#load-resources').click()
    await page.locator('#resource-rows tr').nth(2).wait_for(state='attached')
    return page

async def test_plan_response_is_discarded(browser, fixture):
    page = await loaded_page(browser, fixture)
    entered, release, fulfilled = asyncio.Event(), asyncio.Event(), asyncio.Event()
    async def hold_plan(route):
        response = await route.fetch()
        entered.set()
        await release.wait()
        await route.fulfill(response=response)
        fulfilled.set()
    await page.route('**/api/plans', hold_plan)
    await page.locator('#resource-rows .resource-check').nth(0).check()
    await page.locator('#build-plan').click()
    await asyncio.wait_for(entered.wait(), timeout=8000)
    # A different selection invalidates the plan while its request is in flight.
    await page.locator('#resource-rows .resource-check').nth(1).check()
    pending_disabled = await page.locator('#build-plan').is_disabled()
    release.set()
    await asyncio.wait_for(fulfilled.wait(), timeout=8000)
    await page.wait_for_load_state('networkidle')
    deploy = page.locator('#deploy-button')
    assert await page.locator('#plan-content').is_hidden(), 'stale plan response replaced the invalidated review state'
    assert await deploy.is_disabled(), 'stale plan response re-enabled Deploy after the selection changed'
    assert pending_disabled, 'changed selection allowed a second draft while its first request was pending'
    await expect(page.locator('#build-plan')).to_be_enabled()
    await page.close()

async def test_inventory_response_is_discarded(browser, fixture):
    page = await start_page(browser, fixture)
    entered, release = asyncio.Event(), asyncio.Event()
    count = 0
    async def hold_inventory(route):
        nonlocal count
        response = await route.fetch()
        count += 1
        if count == 2:
            entered.set()
        await release.wait()
        await route.fulfill(response=response)
    await page.route('**/api/inventory', hold_inventory)
    await page.locator('#load-resources').click()
    await asyncio.wait_for(entered.wait(), timeout=8000)
    # Switching source invalidates both inventory requests already in flight.
    await page.locator('#source-profile').select_option(fixture['target'])
    release.set()
    await page.wait_for_timeout(150)
    assert await page.locator('#resource-table-wrap').is_hidden(), 'stale inventory response populated resources for the old profile pair'
    assert await page.locator('#resource-state h3').inner_text() == 'Resources have not been loaded'
    await page.close()

async def test_automatic_mapping_override_is_sent(browser, fixture):
    page = await loaded_page(browser, fixture)
    mapping = page.locator('#resource-rows .mapping-select').nth(0)
    await mapping.select_option('')
    captured = {}
    async def inspect_plan(route):
        captured['body'] = json.loads(route.request.post_data)
        await route.continue_()
    await page.route('**/api/plans', inspect_plan)
    await page.locator('#resource-rows .resource-check').nth(0).check()
    await page.locator('#build-plan').click()
    await page.locator('#plan-content').wait_for(state='visible', timeout=8000)
    assert 'f' not in captured['body']['mappings'], 'automatic/create override was replaced by the saved destination suggestion'
    await page.close()

async def test_inventory_failure_recovers_on_retry(browser, fixture):
    page = await start_page(browser, fixture)
    failed_once = False
    async def fail_first_inventory(route):
        nonlocal failed_once
        if not failed_once and route.request.post_data_json['profileId'] == fixture['source']:
            failed_once = True
            await route.fulfill(status=502, json={'error': 'fixture inventory failure'})
        else:
            await route.continue_()
    await page.route('**/api/inventory', fail_first_inventory)
    await page.locator('#load-resources').click()
    await expect(page.locator('#resource-state h3')).to_have_text('Could not load resources')
    await expect(page.locator('#resource-state')).to_have_attribute('role', 'alert')
    await expect(page.locator('#resource-state')).to_contain_text('Source inventory')
    await expect(page.locator('#plan-state')).to_contain_text('Load both inventories successfully')
    await expect(page.locator('#load-resources')).to_be_enabled()
    await page.locator('#load-resources').click()
    await expect(page.locator('#resource-rows tr')).to_have_count(3)
    await page.close()

async def test_empty_inventory_has_actionable_state(browser, fixture):
    page = await start_page(browser, fixture)
    async def empty_inventory(route):
        response = await route.fetch()
        body = await response.json()
        body['resources'] = []
        await route.fulfill(response=response, json=body)
    await page.route('**/api/inventory', empty_inventory)
    await page.locator('#load-resources').click()
    await expect(page.locator('#resource-state h3')).to_have_text('No resources found')
    await expect(page.locator('#build-plan')).to_be_disabled()
    await expect(page.locator('#load-resources')).to_be_enabled()
    await page.close()

async def test_keyboard_can_select_resource_and_close_dialog(browser, fixture):
    page = await loaded_page(browser, fixture)
    checkbox = page.locator('#resource-rows .resource-check').nth(0)
    await checkbox.focus()
    await page.keyboard.press('Space')
    await expect(checkbox).to_be_checked()
    await expect(page.locator('#selection-count')).to_have_text('1 selected')
    await page.locator('#profiles-open').click()
    await expect(page.locator('#profiles-dialog')).to_be_visible()
    await page.keyboard.press('Escape')
    await expect(page.locator('#profiles-dialog')).to_be_hidden()
    await page.close()

async def test_profile_close_bypasses_validation_without_saving(browser, fixture):
    page = await start_page(browser, fixture)
    writes, errors = [], []
    page.on('pageerror', lambda error: errors.append(str(error)))
    async def capture_profile_write(route):
        if route.request.method != 'GET':
            writes.append(route.request.method)
        await route.continue_()
    await page.route('**/api/profiles', capture_profile_write)
    dialog = page.locator('#profiles-dialog')
    for mode in ('session', 'env'):
        await page.locator('#profiles-open').click()
        await dialog.locator('[name="secretMode"]').select_option(mode)
        if mode == 'env':
            await dialog.locator('[name="secretEnv"]').fill('')
        await dialog.get_by_role('button', name='Close environment profiles', exact=True).click()
        await expect(dialog).to_be_hidden()
    # Cancel must not disable validation on the separate Save action.
    await page.locator('#profiles-open').click()
    await page.locator('#profile-save').click()
    await expect(dialog).to_be_visible()
    assert not writes, 'closing or saving an invalid profile sent a mutation'
    assert not errors, errors
    await page.keyboard.press('Escape')
    await page.close()

async def test_gitlab_close_bypasses_validation_without_saving(browser, fixture):
    page = await start_page(browser, fixture)
    writes, errors = [], []
    page.on('pageerror', lambda error: errors.append(str(error)))
    async def capture_gitlab_write(route):
        if route.request.method != 'GET':
            writes.append(route.request.method)
        await route.continue_()
    await page.route('**/api/gitlab', capture_gitlab_write)
    dialog = page.locator('#gitlab-dialog')
    for label in ('Close GitLab settings', 'Close'):
        await page.locator('#gitlab-open').click()
        await expect(dialog).to_be_visible()
        await dialog.locator('[name="baseUrl"]').fill('not a URL')
        await dialog.locator('[name="projectId"]').fill('')
        await dialog.get_by_role('button', name=label, exact=True).click()
        await expect(dialog).to_be_hidden()
    await page.locator('#gitlab-open').click()
    await dialog.locator('[name="baseUrl"]').fill('not a URL')
    await page.locator('#gitlab-save').click()
    await expect(dialog).to_be_visible()
    assert not writes, 'closing or saving invalid GitLab settings sent a mutation'
    assert not errors, errors
    await page.keyboard.press('Escape')
    await page.close()

async def test_description_change_discards_inflight_plan(browser, fixture):
    page = await loaded_page(browser, fixture)
    await page.locator('#plan-description').fill('Reviewed description')
    entered, release = asyncio.Event(), asyncio.Event()
    async def hold_plan(route):
        response = await route.fetch()
        entered.set()
        await release.wait()
        await route.fulfill(response=response)
    await page.route('**/api/plans', hold_plan)
    await page.locator('#resource-rows .resource-check').nth(0).check()
    await page.locator('#build-plan').click()
    await asyncio.wait_for(entered.wait(), timeout=8000)
    await page.locator('#plan-description').fill('Changed while review was building')
    pending_disabled = await page.locator('#build-plan').is_disabled()
    release.set()
    await page.wait_for_load_state('networkidle')
    assert await page.locator('#plan-content').is_hidden(), 'stale plan response replaced the invalidated description review state'
    assert await page.locator('#deploy-button').is_disabled(), 'stale plan response allowed deployment after the description changed'
    assert pending_disabled, 'changed description allowed a second draft while its first request was pending'
    await expect(page.locator('#build-plan')).to_be_enabled()
    await page.close()

async def test_history_scroll_region_is_keyboard_accessible(browser, fixture):
    page = await loaded_page(browser, fixture)
    await page.set_viewport_size({'width':900, 'height':1000})
    await page.locator('#resource-rows .resource-check').nth(0).check()
    await page.locator('#build-plan').click()
    await expect(page.locator('#plan-content')).to_be_visible()
    await page.locator('#refresh-history').click()
    await expect(page.locator('#history-table-wrap')).to_be_visible()
    assert await page.evaluate('document.documentElement.scrollWidth <= innerWidth'), 'activity-table headers escaped their scroll container'
    await expect(page.locator('.history-table').get_by_role('columnheader', name='Actions', exact=True)).to_have_count(1)
    history = page.get_by_role('region', name='Scrollable promotion history', exact=True)
    await expect(history).to_have_count(1)
    assert await history.evaluate('(el) => el.scrollWidth > el.clientWidth'), 'fixture must exercise horizontal overflow'
    await history.focus()
    await expect(history).to_be_focused()
    await page.keyboard.press('ArrowRight')
    await expect(history).not_to_have_js_property('scrollLeft', 0)
    await page.close()

async def test_editing_target_profile_preserves_environment_roles(browser, fixture):
    page = await start_page(browser, fixture)
    source_id, target_id = fixture['source'], fixture['target']
    await page.locator('#profiles-open').click()
    target_row = page.locator('.profile-list-item').filter(has_text='Production')
    await expect(target_row).to_have_count(1)
    await target_row.get_by_role('button', name='Edit').click()
    name = page.locator('#profiles-form [name="name"]')
    await name.fill('Production renamed')
    await page.locator('#profiles-form [name="authEnvironmentId"]').fill('dst')
    async with page.expect_response(lambda response: response.url.endswith('/api/profiles') and response.request.method == 'POST') as saved:
        await page.locator('#profile-save').click()
    assert (await saved.value).ok, 'profile edit must actually reach persistence'
    await expect(page.locator('#profile-editor-summary')).to_have_text('Add an environment profile')
    await expect(page.locator('#source-profile')).to_have_value(source_id)
    await expect(page.locator('#target-profile')).to_have_value(target_id)
    await page.close()

async def test_profile_failure_does_not_escape_edit_session(browser, fixture):
    page = await start_page(browser, fixture)
    await page.locator('#profiles-open').click()
    await page.locator('.profile-list-item').filter(has_text='Production').get_by_role('button', name='Edit').click()
    await page.locator('#profiles-form [name="authEnvironmentId"]').fill('dst')
    entered, release = asyncio.Event(), asyncio.Event()
    async def fail_save(route):
        entered.set()
        await release.wait()
        await route.fulfill(status=500, json={'error': 'old profile save failed'})
    await page.route('**/api/profiles', fail_save)
    await page.locator('#profile-save').click()
    await asyncio.wait_for(entered.wait(), timeout=8)
    await page.keyboard.press('Escape')
    await page.locator('#profiles-open').click()
    await page.locator('#profiles-form [name="name"]').fill('New edit session')
    release.set()
    await page.wait_for_load_state('networkidle')
    await expect(page.locator('#profile-error')).to_be_hidden()
    await expect(page.locator('#profiles-form [name="name"]')).to_have_value('New edit session')
    # The old failure still needs to be reported, with its operation context.
    await expect(page.locator('#toast-region')).to_contain_text('Production')
    await expect(page.locator('#toast-region')).to_contain_text('old profile save failed')
    await page.close()

async def test_uncertain_deploy_cannot_be_reenabled(browser, fixture):
    page = await loaded_page(browser, fixture)
    await page.locator('#resource-rows .resource-check').nth(0).check()
    await page.locator('#build-plan').click()
    await expect(page.locator('#deploy-button')).to_be_enabled()
    await page.locator('#deploy-button').click()
    entered, release = asyncio.Event(), asyncio.Event()
    attempts = []
    async def lose_response(route):
        attempts.append(route.request.url)
        entered.set()
        await release.wait()
        await route.fulfill(status=502, json={'error': 'fixture response lost'})
    await page.route('**/api/plans/*/deploy', lose_response)
    check = page.locator('#deploy-confirm-check')
    await check.check()
    await page.locator('#confirm-deploy').click()
    await asyncio.wait_for(entered.wait(), timeout=8)
    # Even before a response, changing the acknowledgement must not enable a duplicate write.
    await check.uncheck()
    await check.check()
    pending_disabled = await page.locator('#confirm-deploy').is_disabled()
    release.set()
    await expect(page.locator('#deploy-error')).to_contain_text('PingOne may have received it')
    await check.uncheck()
    await check.check()
    await expect(page.locator('#confirm-deploy')).to_be_disabled()
    assert pending_disabled, 'acknowledgement re-enabled a deployment while its request was pending'
    assert len(attempts) == 1, 'uncertain deployment was submitted more than once'
    await page.keyboard.press('Escape')
    await page.locator('#deploy-button').click()
    await check.check()
    await expect(page.locator('#confirm-deploy')).to_be_disabled()
    await expect(page.locator('#deploy-error')).to_contain_text('PingOne may have received it')
    await page.close()

async def test_invalid_response_is_not_an_empty_inventory(browser, fixture):
    for status in (200, 502):
        page = await start_page(browser, fixture)
        async def invalid_inventory(route):
            await route.fulfill(status=status, content_type='text/html', body='<html>RAW_PROXY_DIAGNOSTIC</html>')
        await page.route('**/api/inventory', invalid_inventory)
        await page.locator('#load-resources').click()
        await expect(page.locator('#resource-state h3')).to_have_text('Could not load resources')
        await expect(page.locator('#resource-state')).to_contain_text('invalid response')
        await expect(page.locator('#resource-state')).not_to_contain_text('RAW_PROXY_DIAGNOSTIC')
        await expect(page.locator('#build-plan')).to_be_disabled()
        await page.close()

async def test_native_preflight_failure_allows_explicit_retry(browser, fixture):
    page = await loaded_page(browser, fixture)
    await page.locator('#resource-rows .resource-check').nth(0).check()
    await page.locator('#build-plan').click()
    await expect(page.locator('#deploy-button')).to_be_enabled()
    await page.locator('#deploy-button').click()
    attempts = []
    async def fail_before_write(route):
        attempts.append(route.request.url)
        await route.fulfill(status=500, json={'error':'Execution preflight failed; no PingOne execution was sent.', 'safeToRetry':True})
    await page.route('**/api/plans/*/deploy', fail_before_write)
    await page.locator('#deploy-confirm-check').check()
    await page.locator('#confirm-deploy').click()
    await expect(page.locator('#deploy-error')).to_be_visible()
    assert len(attempts) == 1, 'preflight error caused an automatic execution retry'
    await expect(page.locator('#deploy-error')).not_to_contain_text('PingOne may have received it')
    await expect(page.locator('#confirm-deploy')).to_be_enabled()
    await page.locator('#confirm-deploy').click()
    await expect(page.locator('#confirm-deploy')).to_be_enabled()
    assert len(attempts) == 2, 'proven pre-write failure did not allow an explicit retry'
    await page.close()

    # A rejected stale review is also pre-write, but needs another review before retry.
    page = await loaded_page(browser, fixture)
    await page.locator('#resource-rows .resource-check').nth(0).check()
    await page.locator('#build-plan').click()
    await expect(page.locator('#deploy-button')).to_be_enabled()
    await page.locator('#deploy-button').click()
    async def reject_stale_review(route):
        await route.fulfill(status=409, json={'error':'The native plan changed; build a fresh review.', 'mutationStarted':False})
    await page.route('**/api/plans/*/deploy', reject_stale_review)
    await page.locator('#deploy-confirm-check').check()
    await page.locator('#confirm-deploy').click()
    await expect(page.locator('#deploy-error')).to_contain_text('build a fresh review')
    await expect(page.locator('#deploy-error')).not_to_contain_text('PingOne may have received it')
    await expect(page.locator('#confirm-deploy')).to_be_disabled()
    await expect(page.locator('#confirm-deploy')).to_have_text('Review required')
    await page.close()

async def test_stale_draft_error_keeps_reconciliation_guidance(browser, fixture):
    page = await loaded_page(browser, fixture)
    entered, release = asyncio.Event(), asyncio.Event()
    async def fail_draft(route):
        entered.set()
        await release.wait()
        await route.fulfill(status=502, json={'error':'PingOne may have created a draft; reconcile promotions before trying again.'})
    await page.route('**/api/plans', fail_draft)
    await page.locator('#resource-rows .resource-check').nth(0).check()
    await page.locator('#build-plan').click()
    await asyncio.wait_for(entered.wait(), timeout=8)
    await page.locator('#resource-rows .resource-check').nth(1).check()
    release.set()
    await page.wait_for_load_state('networkidle')
    await expect(page.locator('#plan-state')).to_have_attribute('role', 'alert')
    await expect(page.locator('#plan-state')).to_contain_text('Previous review request failed')
    await expect(page.locator('#plan-state')).to_contain_text('reconcile promotions')
    await expect(page.locator('#plan-state')).to_contain_text('Development')
    await expect(page.locator('#plan-content')).to_be_hidden()
    await page.close()

async def main():
    fixture = json.loads(FIXTURE.read_text())
    for endpoint in ('url','native'):
        host=urllib.parse.urlparse(fixture[endpoint]).hostname
        if host!='localhost' and not ipaddress.ip_address(host).is_loopback:
            raise ValueError('Tests require a loopback-only fixture')
    outcomes = []
    async with async_playwright() as p:
        browser = await p.chromium.launch(headless=True, executable_path=args.chromium, args=['--no-sandbox'])
        for name, test in [
            ('plan response generation', test_plan_response_is_discarded),
            ('inventory response generation', test_inventory_response_is_discarded),
            ('automatic mapping request body', test_automatic_mapping_override_is_sent),
            ('inventory service failure retry', test_inventory_failure_recovers_on_retry),
            ('empty inventory state', test_empty_inventory_has_actionable_state),
            ('keyboard selection and dialog close', test_keyboard_can_select_resource_and_close_dialog),
            ('profile close with invalid credential fields', test_profile_close_bypasses_validation_without_saving),
            ('GitLab close with invalid fields', test_gitlab_close_bypasses_validation_without_saving),
            ('description response generation', test_description_change_discards_inflight_plan),
            ('keyboard scrollable activity region', test_history_scroll_region_is_keyboard_accessible),
            ('edited profile role preservation', test_editing_target_profile_preserves_environment_roles),
            ('profile error edit-session isolation', test_profile_failure_does_not_escape_edit_session),
            ('uncertain deploy remains locked', test_uncertain_deploy_cannot_be_reenabled),
            ('invalid API response errors', test_invalid_response_is_not_an_empty_inventory),
            ('native preflight permits explicit retry', test_native_preflight_failure_allows_explicit_retry),
            ('stale draft errors retain reconciliation', test_stale_draft_error_keeps_reconciliation_guidance),
        ]:
            try:
                await test(browser, fixture)
                outcomes.append((name, 'PASS'))
            except Exception as error:
                outcomes.append((name, f'FAIL: {type(error).__name__}: {error}'))
        await browser.close()
    for name, result in outcomes:
        print(f'{name}: {result}')
    failures = [result for _, result in outcomes if result != 'PASS']
    if failures:
        raise SystemExit(1)

if __name__ == '__main__':
    asyncio.run(main())
