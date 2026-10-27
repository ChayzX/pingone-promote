"""Fixture-only browser regressions for GitLab production preview and stale response handling."""
import asyncio, json, argparse, ipaddress
from urllib.parse import urlparse
from playwright.async_api import async_playwright

p = argparse.ArgumentParser()
p.add_argument('--fixture-file', required=True)
p.add_argument('--chromium', default='/usr/bin/chromium')
args = p.parse_args()
fixture = json.load(open(args.fixture_file))
for endpoint in ('url','native'):
    host=urlparse(fixture[endpoint]).hostname
    if host!='localhost' and not ipaddress.ip_address(host).is_loopback:
        raise ValueError('Browser regression requires a loopback test fixture')

async def setup(page):
    async def session_with_production_policy(route):
        response = await route.fetch()
        data = await response.json()
        data['localTargets'] = []
        await route.fulfill(response=response, json=data)
    await page.route('**/api/session', session_with_production_policy)
    await page.route('**/api/gitlab', lambda route: route.fulfill(json={"config":{"baseUrl":"https://gitlab.example.test","projectId":"42","tokenEnv":"GITLAB_TOKEN","environment":"production"},"configured":True,"tokenAvailable":True}))
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
    async def plans(route):
        calls['plans'] += 1
        await route.continue_()
    async def preview(route):
        calls['preview'] += 1
        body = json.loads(route.request.post_data)
        await route.fulfill(json={"gitlabRequired":True,"issues":[],"request":{"version":1,"id":"0123456789abcdef0123456789abcdef","createdAt":"2026-10-27T00:00:00Z","source":{"id":"src-env","region":"us","organizationId":"org-s","snapshotId":"snap-src"},"target":{"id":"prod-env","region":"us","organizationId":"org-p","snapshotId":"snap-prod"},"selected":body['selected'],"mappings":body['mappings'],"configurations":{"f":{"name":"Customer login","secret":"[REDACTED]"}},"fingerprints":{"f":"sha256:abc"}}})
    async def submit(route):
        calls['submit'] += 1
        assert json.loads(route.request.post_data) == {'requestId':'0123456789abcdef0123456789abcdef'}
        await route.fulfill(json={"requestId":"req-1","url":"https://gitlab.example.test/group/project/-/merge_requests/7","branch":"promote/0123456789abcdef0123456789abcdef","iid":7})
    await page.route('**/api/plans', plans)
    await page.route('**/api/requests/preview', preview)
    await page.route('**/api/gitlab/submit', submit)
    await page.locator('#build-plan').click()
    await page.get_by_role('button', name='Submit to GitLab').wait_for()
    assert calls['preview']==1 and calls['plans']==0
    assert await page.locator('#deploy-bar').is_hidden()
    assert await page.locator('#plan-content').inner_text() and 'snap-src' in await page.locator('#plan-content').inner_text()
    await page.get_by_role('button', name='Submit to GitLab').click()
    await page.get_by_label('I reviewed the operations and want to continue.').check()
    await page.locator('#confirm-deploy').click()
    await page.get_by_role('link', name='promote/0123456789abcdef0123456789abcdef !7').wait_for()
    assert calls['submit']==1 and calls['plans']==0
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

async def main():
    async with async_playwright() as pw:
        browser=await pw.chromium.launch(headless=True, executable_path=args.chromium, args=['--no-sandbox'])
        await test_preview_submit(browser)
        await test_stale_preview_invalidated(browser)
        await browser.close()
    print('PASS: GitLab production preview/submission and stale preview invalidation')

asyncio.run(main())
