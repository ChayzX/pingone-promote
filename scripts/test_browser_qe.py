"""QE regression checks for the local UI.

Start the loopback fixture (never contacts PingOne) from the repository root:

  QE_FIXTURE_FILE=/tmp/pingone-qe.json QE_DATA_DIR=$(mktemp -d) \
    go test ./internal/app -run TestQEFixture -v -timeout=60m

then run:  python3 scripts/test_browser_qe.py --fixture-file /tmp/pingone-qe.json
"""
import argparse, json, ssl, sys, urllib.request, uuid
from pathlib import Path
from playwright.sync_api import sync_playwright, expect

parser = argparse.ArgumentParser()
parser.add_argument('--fixture-file', required=True)
parser.add_argument('--output-dir', default='/tmp/pingone-qe-evidence')
parser.add_argument('--chromium', default=None)
args = parser.parse_args()
info = json.load(open(args.fixture_file))
out = Path(args.output_dir); out.mkdir(parents=True, exist_ok=True)
CHECKS = []
RUN = uuid.uuid4().hex[:6]  # profiles persist in the fixture store; keep names unique per run


def check(fn):
    CHECKS.append(fn)
    return fn


def cfg(**kw):
    req = urllib.request.Request(info['native'] + '/__fixture/config', data=json.dumps(kw).encode(), method='POST')
    urllib.request.urlopen(req, context=ssl._create_unverified_context()).read()  # loopback fixture only


def fill_profile(page, name, env, secret='s3cret'):
    form = page.locator('#profiles-form')
    form.locator('[name=name]').fill(name)
    form.locator('[name=environmentId]').fill(env)
    form.locator('[name=authEnvironmentId]').fill('auth1')
    form.locator('[name=clientId]').fill('worker')
    form.locator('[name=secret]').fill(secret)


def create_profile(page, name, env):
    page.get_by_role('button', name='Environment profiles').click()
    fill_profile(page, name, env)
    page.locator('#profile-save').click()
    expect(page.locator('#profile-list')).to_contain_text(name)
    page.locator('#profiles-close').click()


def setup_pair(page):
    suffix = ' ' + RUN
    create_profile(page, 'Dev' + suffix, 'src')
    create_profile(page, 'Prod' + suffix, 'dst')
    page.locator('#source-profile').select_option(label='Dev%s · src' % suffix)
    page.locator('#target-profile').select_option(label='Prod%s · dst' % suffix)


def load_resources(page):
    page.get_by_role('button', name='Load resources', exact=True).click()
    expect(page.locator('#resource-rows tr').first).to_be_visible()


def build_plan(page):
    page.get_by_role('button', name='Build review', exact=True).click()
    expect(page.locator('#plan-content')).to_be_visible()


@check
def enter_key_saves_profile_instead_of_closing_dialog(page):
    page.get_by_role('button', name='Environment profiles').click()
    fill_profile(page, 'Enter key profile', 'src')
    page.locator('#profiles-form [name=name]').press('Enter')
    expect(page.locator('#profile-list')).to_contain_text('Enter key profile')
    assert page.locator('#profiles-dialog').evaluate('d => d.open'), 'dialog closed by Enter'


@check
def editing_destination_profile_keeps_source_and_destination(page):
    create_profile(page, 'Edit Dev ' + RUN, 'src')
    create_profile(page, 'Edit Prod ' + RUN, 'dst')
    page.locator('#source-profile').select_option(label='Edit Dev %s · src' % RUN)
    page.locator('#target-profile').select_option(label='Edit Prod %s · dst' % RUN)
    page.get_by_role('button', name='Environment profiles').click()
    page.locator('.profile-list-item', has_text='Edit Prod ' + RUN).get_by_role('button', name='Edit').click()
    page.locator('#profiles-form [name=name]').fill('Edit Prod renamed ' + RUN)
    page.locator('#profile-save').click()
    expect(page.locator('#profile-list')).to_contain_text('Edit Prod renamed ' + RUN)
    page.locator('#profiles-close').click()
    expect(page.locator('#source-profile option:checked')).to_have_text('Edit Dev %s · src' % RUN)
    expect(page.locator('#target-profile option:checked')).to_have_text('Edit Prod renamed %s · dst' % RUN)


@check
def review_step_urls_are_shown_verbatim_not_uppercased(page):
    cfg(names=['Customer login'], count=3)
    setup_pair(page)
    load_resources(page)
    page.get_by_role('checkbox', name='Select Customer login for promotion').check()
    build_plan(page)
    url = page.locator('.step-url').last
    expect(url).to_contain_text('/v1/environments/dst/flows/d000')
    assert url.evaluate('e => getComputedStyle(e).textTransform') == 'none', 'URL is rendered with text-transform'
    page.screenshot(path=str(out / 'step-url.png'), full_page=True)


def main():
    failed = 0
    with sync_playwright() as p:
        launch = {'executable_path': args.chromium} if args.chromium else {}
        browser = p.chromium.launch(**launch)
        for fn in CHECKS:
            page = browser.new_context(viewport={'width': 1440, 'height': 1000}, reduced_motion='reduce').new_page()
            errors = []
            page.on('pageerror', lambda e: errors.append(str(e)))
            page.goto(info['url'])
            page.wait_for_load_state('networkidle')
            try:
                fn(page)
                assert not errors, errors
                print('PASS:', fn.__name__)
            except Exception as e:
                failed += 1
                print('FAIL:', fn.__name__, '-', str(e).splitlines()[0])
                page.screenshot(path=str(out / (fn.__name__ + '.png')), full_page=True)
            page.context.close()
        browser.close()
    sys.exit(1 if failed else 0)


main()
