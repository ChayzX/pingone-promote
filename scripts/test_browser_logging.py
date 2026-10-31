"""Session-scoped logging settings regressions against the local fixture."""
import argparse
import json
import tempfile
from pathlib import Path

from playwright.sync_api import expect, sync_playwright


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--fixture-file', required=True)
    parser.add_argument('--chromium', required=True)
    parser.add_argument('--output-dir', required=True)
    args = parser.parse_args()
    with open(args.fixture_file, encoding='utf-8') as fixture_file:
        fixture = json.load(fixture_file)
    output = Path(args.output_dir)
    output.mkdir(parents=True, exist_ok=True)

    with sync_playwright() as playwright:
        browser = playwright.chromium.launch(executable_path=args.chromium, headless=True)
        context = browser.new_context(viewport={'width': 1280, 'height': 900}, reduced_motion='reduce')
        page = context.new_page()
        errors = []
        page.on('pageerror', lambda error: errors.append(str(error)))
        page.goto(fixture['url'])
        page.locator('#source-profile option').nth(1).wait_for(state='attached')

        page.get_by_role('button', name='Logging settings').click()
        dialog = page.get_by_role('dialog', name='Logging settings')
        expect(dialog).to_be_visible()
        expect(page.locator('#logging-current')).to_contain_text('info · json · standard error')
        page.locator('#logging-debug').check()
        expect(page.locator('#logging-level')).to_be_disabled()
        for width, height in [(1280, 900), (900, 900), (390, 844)]:
            page.set_viewport_size({'width': width, 'height': height})
            assert page.evaluate('document.documentElement.scrollWidth <= window.innerWidth'), f'horizontal overflow at {width}'
            page.screenshot(path=str(output / f'logging-settings-{width}.png'), full_page=True)
        page.get_by_role('button', name='Cancel', exact=True).click()
        expect(dialog).to_be_hidden()
        effective = page.evaluate("fetch('/api/logging').then(response => response.json())")
        assert effective['debug'] is False and effective['level'] == 'info', 'Cancel applied the unsaved debug toggle'

        page.get_by_role('button', name='Logging settings').click()
        page.locator('#logging-debug').check()
        page.get_by_role('button', name='Close logging settings').click()
        expect(dialog).to_be_hidden()
        effective = page.evaluate("fetch('/api/logging').then(response => response.json())")
        assert effective['debug'] is False and effective['level'] == 'info', 'Close applied the unsaved debug toggle'

        csrf_status = page.evaluate("""async () => {
          const response = await fetch('/api/logging', {
            method: 'PUT', headers: {'Content-Type': 'application/json'},
            body: JSON.stringify({debug: true, level: 'debug', format: 'json', destination: 'stderr', file: ''})
          });
          return response.status;
        }""")
        assert csrf_status == 403, f'logging settings accepted a PUT without CSRF (HTTP {csrf_status})'

        with tempfile.TemporaryDirectory(prefix='pingone-log-ui-') as temp_dir:
            page.get_by_role('button', name='Logging settings').click()
            page.locator('#logging-debug').check()
            page.locator('#logging-format').select_option('text')
            settings = page.evaluate("fetch('/api/logging').then(response => response.json())")
            if settings['fileSupported']:
                log_path = str(Path(temp_dir) / 'events.log')
                page.locator('#logging-destination').select_option('file')
                page.locator('#logging-file').fill(log_path)
            else:
                assert page.locator('#logging-destination option[value="file"]').evaluate('(option) => option.disabled'), 'Windows file destination is not disabled'
                expect(page.locator('#logging-file-support-note')).to_be_visible()
                expect(page.locator('#logging-file-support-note')).to_contain_text('private file ACL')

            with page.expect_response(lambda response: response.url.endswith('/api/logging') and response.request.method == 'PUT') as saved:
                page.get_by_role('button', name='Save changes').click()
            assert saved.value.status == 200, f'logging settings save failed: HTTP {saved.value.status}'
            expect(dialog).to_be_hidden()
            effective = page.evaluate("fetch('/api/logging').then(response => response.json())")
            assert effective['debug'] is True and effective['level'] == 'debug' and effective['format'] == 'text', effective
            if settings['fileSupported']:
                assert effective['destination'] == 'file' and effective['file'] == log_path, effective
                assert Path(log_path).exists(), 'the selected log file was not created'
                page.get_by_role('button', name='Logging settings').click()
                page.locator('#logging-destination').select_option('stderr')
                page.get_by_role('button', name='Save changes').click()
                expect(dialog).to_be_hidden()
                effective = page.evaluate("fetch('/api/logging').then(response => response.json())")
                assert effective['destination'] == 'stderr' and effective['file'] == '', effective

            def fail_log_save(route):
                if route.request.method == 'PUT':
                    return route.fulfill(status=400, content_type='application/json', body=json.dumps({'error': 'Could not open the requested destination; choose a writable regular file and retry. Current settings are unchanged.'}))
                return route.continue_()

            page.route('**/api/logging', fail_log_save)
            page.get_by_role('button', name='Logging settings').click()
            page.locator('#logging-format').select_option('json')
            page.get_by_role('button', name='Save changes').click()
            expect(dialog).to_be_visible()
            expect(page.locator('#logging-error')).to_contain_text('choose a writable regular file and retry')
            page.unroute('**/api/logging', fail_log_save)
            page.get_by_role('button', name='Cancel', exact=True).click()
            effective = page.evaluate("fetch('/api/logging').then(response => response.json())")
            assert effective['format'] == 'text', 'failed save or Cancel changed the effective format'

        assert not errors, errors
        print('PASS: settings are authenticated/CSRF protected; debug/destination saves work; cancel and failed save preserve effective settings')
        browser.close()


if __name__ == '__main__':
    main()
