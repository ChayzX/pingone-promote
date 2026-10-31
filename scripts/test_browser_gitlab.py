"""Fixture-only browser regressions for GitLab production preview and stale response handling."""
import asyncio, json, argparse, ipaddress
from urllib.parse import urlparse
from playwright.async_api import async_playwright, expect

p = argparse.ArgumentParser()
p.add_argument('--fixture-file', required=True)
p.add_argument('--chromium', default='/usr/bin/chromium')
args = p.parse_args()
fixture = json.load(open(args.fixture_file))
for endpoint in ('url','native'):
    host=urlparse(fixture[endpoint]).hostname
    if host!='localhost' and not ipaddress.ip_address(host).is_loopback:
        raise ValueError('Browser regression requires a loopback test fixture')

async def setup(page, gitlab_state=None):
    async def session_with_production_policy(route):
        response = await route.fetch()
        data = await response.json()
        data['localTargets'] = []
        await route.fulfill(response=response, json=data)
    await page.route('**/api/session', session_with_production_policy)
    state = gitlab_state or {"config":{"baseUrl":"https://gitlab.example.test","projectId":"42","tokenEnv":"GITLAB_TOKEN","environment":"production"},"configured":True,"tokenAvailable":True}
    await page.route('**/api/gitlab', lambda route: route.fulfill(json=state))
    await page.goto(fixture['url'])
    await page.locator('#source-profile option').nth(1).wait_for(state='attached')
    await page.locator('#source-profile').select_option(fixture['source'])
    await page.locator('#target-profile').select_option(fixture['target'])
    await page.locator('#load-resources').click()
    await page.locator('#resource-rows tr').nth(2).wait_for(state='attached')
    await page.locator('.resource-check').nth(0).check()

async def test_preview_submit(browser):
    page = await browser.new_page()
    await setup(page)
    calls = {'plans':0, 'preview':0, 'submit':0}
    preview_entered, release_preview = asyncio.Event(), asyncio.Event()
    async def plans(route):
        calls['plans'] += 1
        await route.continue_()
    async def preview(route):
        calls['preview'] += 1
        body = json.loads(route.request.post_data)
        preview_entered.set()
        await release_preview.wait()
        await route.fulfill(json={"gitlabRequired":True,"issues":[],"request":{"version":1,"id":"0123456789abcdef0123456789abcdef","createdAt":"2026-10-27T00:00:00Z","source":{"id":"src-env","region":"us","organizationId":"org-s","snapshotId":"snap-src"},"target":{"id":"prod-env","region":"us","organizationId":"org-p","snapshotId":"snap-prod"},"selected":body['selected'],"mappings":body['mappings'],"configurations":{"f":{"name":"Customer login","secret":"[REDACTED]"}},"fingerprints":{"f":"sha256:abc"}}})
    async def submit(route):
        calls['submit'] += 1
        assert json.loads(route.request.post_data) == {'requestId':'0123456789abcdef0123456789abcdef','branchName':'requests/custom-review','commitMessage':'Review promotion request'}
        await route.fulfill(json={"requestId":"req-1","url":"https://gitlab.example.test/group/project/-/merge_requests/7","branch":"requests/custom-review","iid":7})
    await page.route('**/api/plans', plans)
    await page.route('**/api/requests/preview', preview)
    await page.route('**/api/gitlab/submit', submit)
    await page.locator('#build-plan').click()
    await asyncio.wait_for(preview_entered.wait(), 8)
    await expect(page.locator('#build-plan')).to_have_text('Preparing request…')
    await expect(page.locator('#plan-state')).to_contain_text('Preparing GitLab request')
    await expect(page.locator('#plan-state')).to_contain_text('No PingOne plan or changes')
    release_preview.set()
    await page.get_by_role('button', name='Submit to GitLab').wait_for()
    assert calls['preview']==1 and calls['plans']==0
    assert await page.locator('#deploy-bar').is_hidden()
    assert await page.locator('#plan-content').inner_text() and 'snap-src' in await page.locator('#plan-content').inner_text()
    await page.get_by_role('button', name='Submit to GitLab').click()
    await page.get_by_label('Branch name').fill('requests/custom-review')
    await page.get_by_label('Commit message').fill('Review promotion request')
    await page.get_by_label('I reviewed the operations and want to continue.').check()
    await page.locator('#confirm-deploy').click()
    await page.get_by_role('link', name='requests/custom-review !7').wait_for()
    assert calls['submit']==1 and calls['plans']==0
    await page.close()

async def test_gitlab_settings_explain_missing_token_and_inputs(browser):
    page = await browser.new_page()
    state = {"config":{"baseUrl":"https://gitlab.example.test","projectId":"42","tokenEnv":"GITLAB_TOKEN","environment":"production"},"configured":True,"tokenAvailable":False}
    await setup(page, state)
    await page.locator('#gitlab-open').click()
    await expect(page.locator('#gitlab-config-state')).to_contain_text('no token is available')
    guide = (await page.locator('.gitlab-setup-guide').inner_text()).lower()
    assert 'settings' in guide and 'general' in guide and 'project id' in guide
    assert 'developer' in guide and 'api' in guide and 'protected default branch' in guide
    token_label = await page.locator('label:has(input[name="token"])').inner_text()
    assert 'optional' not in token_label.lower()
    await page.close()

async def test_stale_preview_invalidated(browser):
    page = await browser.new_page()
    await setup(page)
    entered, release = asyncio.Event(), asyncio.Event()
    async def preview(route):
        entered.set(); await release.wait()
        await route.fulfill(json={"gitlabRequired":True,"issues":[],"request":{"id":"abcdef0123456789abcdef0123456789","selected":["f"],"mappings":{},"source":{},"target":{},"configurations":{},"fingerprints":{}}})
    await page.route('**/api/requests/preview', preview)
    await page.locator('#build-plan').click()
    await asyncio.wait_for(entered.wait(), 8)
    await page.locator('.resource-check').nth(1).check()
    release.set()
    await page.wait_for_load_state('networkidle')
    assert await page.locator('#plan-content').is_hidden(), 'stale GitLab preview replaced invalidated review'
    assert await page.locator('#deploy-bar').is_hidden(), 'stale GitLab preview exposed a local deployment control'
    await page.close()

async def test_submit_disabled_if_either_gitlab_requirement_is_missing(browser):
    states = [
        {"config":{"baseUrl":"https://gitlab.example.test","projectId":"42"},"configured":True,"tokenAvailable":False},
        {"config":{"baseUrl":"https://gitlab.example.test","projectId":"42"},"configured":False,"tokenAvailable":True},
    ]
    for index, state in enumerate(states):
        page = await browser.new_page()
        await setup(page, state)
        async def preview(route):
            await route.fulfill(json={"gitlabRequired":True,"issues":[],"request":{"id":f"{index:032x}","selected":["f"],"mappings":{},"source":{},"target":{},"configurations":{},"fingerprints":{}}})
        await page.route('**/api/requests/preview', preview)
        await page.locator('#build-plan').click()
        button = page.get_by_role('button', name='Submit to GitLab')
        await button.wait_for()
        await expect(button).to_be_disabled()
        await page.close()

async def test_submission_errors_respect_write_stage(browser):
    for safe in (True, False):
        page = await browser.new_page()
        await setup(page)
        calls = []
        async def preview(route):
            await route.fulfill(json={"gitlabRequired":True,"issues":[],"request":{"id":"fedcba9876543210fedcba9876543210","selected":["f"],"mappings":{},"source":{},"target":{},"configurations":{},"fingerprints":{}}})
        async def submit(route):
            calls.append(route.request.post_data)
            await route.fulfill(status=502, json={"error":"Verification failed; no GitLab writes occurred." if safe else "GitLab submission response lost.","safeToRetry":safe})
        await page.route('**/api/requests/preview', preview)
        await page.route('**/api/gitlab/submit', submit)
        await page.locator('#build-plan').click()
        await page.get_by_role('button', name='Submit to GitLab').click()
        await page.locator('#deploy-confirm-check').check()
        await page.locator('#confirm-deploy').click()
        await expect(page.locator('#deploy-error')).to_be_visible()
        assert len(calls) == 1, 'error response caused an automatic mutation retry'
        if safe:
            await expect(page.locator('#deploy-error')).not_to_contain_text('Check GitLab for the request')
            await expect(page.locator('#confirm-deploy')).to_be_enabled()
            await page.locator('#confirm-deploy').click()
            await expect(page.locator('#confirm-deploy')).to_be_enabled()
            assert len(calls) == 2, 'proven pre-write failure did not allow explicit retry'
        else:
            await expect(page.locator('#deploy-error')).to_contain_text('Check GitLab for the request')
            await page.locator('#deploy-confirm-check').uncheck()
            await page.locator('#deploy-confirm-check').check()
            await expect(page.locator('#confirm-deploy')).to_be_disabled()
        await page.close()

async def main():
    async with async_playwright() as pw:
        browser=await pw.chromium.launch(headless=True, executable_path=args.chromium, args=['--no-sandbox'])
        await test_preview_submit(browser)
        await test_gitlab_settings_explain_missing_token_and_inputs(browser)
        await test_stale_preview_invalidated(browser)
        await test_submit_disabled_if_either_gitlab_requirement_is_missing(browser)
        await test_submission_errors_respect_write_stage(browser)
        await browser.close()
    print('PASS: GitLab production preview/submission, stale preview invalidation, missing-prerequisite blocking, and stage-aware submission errors')

asyncio.run(main())
