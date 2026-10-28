import json,ssl,urllib.request
from pathlib import Path
from playwright.sync_api import sync_playwright,expect
import argparse,ipaddress,urllib.parse
parser=argparse.ArgumentParser(description="Exercise the development-only local PingOne fixture (requires Python Playwright).")
parser.add_argument('--fixture-file',required=True)
parser.add_argument('--chromium',default='/usr/bin/chromium')
parser.add_argument('--output-dir',default='/tmp/pingone-browser-evidence')
args=parser.parse_args()
info=json.load(open(args.fixture_file));out=Path(args.output_dir);out.mkdir(parents=True,exist_ok=True)
for endpoint in ('url','native'):
 host=urllib.parse.urlparse(info[endpoint]).hostname
 if host!='localhost' and not ipaddress.ip_address(host).is_loopback: raise ValueError('Tests require a loopback-only fixture')
def hidden(flag):
 # Unverified TLS is confined to the local httptest server's self-signed cert.
 urllib.request.urlopen(info['native']+'/__fixture/hidden?value='+str(flag).lower(),context=ssl._create_unverified_context()).read()
with sync_playwright() as p:
 b=p.chromium.launch(executable_path=args.chromium,headless=True,args=['--no-sandbox'])
 page=b.new_page(viewport={'width':1440,'height':1000},reduced_motion='reduce')
 errors=[];page.on('pageerror',lambda e:errors.append(str(e)))
 page.goto(info['url']);page.locator('#source-profile option:nth-child(2)').wait_for(state='attached')
 assert page.evaluate('getComputedStyle(document.documentElement).colorScheme') == 'dark', 'operations console must use the requested dark color scheme'
 page.locator('#source-profile').select_option(info['source']);page.locator('#target-profile').select_option(info['target'])
 page.get_by_role('button',name='Load resources',exact=True).click()
 expect(page.locator('#resource-rows tr')).to_have_count(3)
 expect(page.locator('.resource-check:checked')).to_have_count(0)
 page.locator('#resource-search').fill('Customer login');expect(page.locator('#resource-rows tr')).to_have_count(1)
 page.get_by_role('checkbox',name='Select Customer login for promotion',exact=True).check()
 page.locator('#resource-search').clear()
 expect(page.get_by_role('checkbox',name='Select Email template for promotion',exact=True)).not_to_be_checked()
 expect(page.get_by_label('Destination mapping for Customer login')).to_have_value('g')
 page.locator('#plan-description').fill('Browser fixture promotion')
 hidden(True)
 page.get_by_role('button',name='Build review',exact=True).click()
 expect(page.locator('#plan-content')).to_contain_text('unknown or unselected')
 expect(page.locator('#deploy-button')).to_be_disabled()
 page.screenshot(path=str(out/'blocked-template-1440.png'),full_page=True)
 print('PASS: friendly-name search, explicit selection/mapping, later-page template blocked')
 hidden(False)
 page.get_by_role('button',name='Build review',exact=True).click()
 expect(page.locator('#deploy-button')).to_be_enabled()
 expect(page.locator('#plan-content')).to_contain_text('Before')
 for width,height in [(1440,1000),(900,1000),(390,844)]:
  page.set_viewport_size({'width':width,'height':height})
  assert page.evaluate('document.documentElement.scrollWidth<=window.innerWidth'),f'overflow at {width}'
  page.screenshot(path=str(out/f'review-{width}.png'),full_page=True)
 page.set_viewport_size({'width':1440,'height':1000})
 page.get_by_role('checkbox',name='Select Identity connector for promotion',exact=True).check()
 expect(page.locator('#deploy-bar')).to_be_hidden()
 page.get_by_role('checkbox',name='Select Identity connector for promotion',exact=True).uncheck()
 page.get_by_role('button',name='Build review',exact=True).click();expect(page.locator('#deploy-button')).to_be_enabled()
 page.locator('#deploy-button').click();expect(page.locator('#confirm-deploy')).to_be_disabled()
 page.get_by_label('I reviewed the operations and want to continue.').check()
 page.locator('#confirm-deploy').click()
 expect(page.locator('#deploy-state-label')).to_contain_text('complete',ignore_case=True)
 expect(page.locator('#deploy-button')).to_be_disabled()
 expect(page.locator('#history-rows')).to_contain_text('COMPLETED')
 print('PASS: changing choices invalidates review; explicit deployment completes and is recorded')
 completed=page.locator('#history-rows tr').filter(has_text='COMPLETED').first
 completed.get_by_role('button',name='Rollback',exact=True).click()
 expect(page.locator('#deploy-warning')).to_contain_text('whole promotion')
 page.get_by_label('I reviewed the operations and want to continue.').check();page.locator('#confirm-deploy').click()
 expect(page.locator('#deploy-button')).to_have_text('Execute rollback')
 expect(page.locator('#deploy-button')).to_be_enabled()
 page.locator('#deploy-button').click();page.get_by_label('I reviewed the operations and want to continue.').check();page.locator('#confirm-deploy').click()
 expect(page.locator('#deploy-state-label')).to_contain_text('complete',ignore_case=True)
 expect(page.locator('#deploy-button')).to_be_disabled()
 expect(page.locator('#history-rows')).to_contain_text('Rollback:')
 page.screenshot(path=str(out/'rollback-completed-1440.png'),full_page=True)
 assert not errors,errors
 print('PASS: rollback draft is separately reviewed/executed; no JavaScript page errors')
 b.close()
