"""Race regressions for the PingOne Promote browser UI.
Run against the local test fixture whose URL is stored in browser-fixture.json.
"""
import asyncio
import json
from pathlib import Path
from playwright.async_api import async_playwright

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
    release.set()
    await asyncio.wait_for(fulfilled.wait(), timeout=8000)
    await page.wait_for_load_state('networkidle')
    deploy = page.locator('#deploy-button')
    assert await page.locator('#plan-content').is_hidden(), 'stale plan response replaced the invalidated review state'
    assert await deploy.is_disabled(), 'stale plan response re-enabled Deploy after the selection changed'
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
