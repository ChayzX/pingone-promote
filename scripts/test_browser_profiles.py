"""Credential-mode regressions against the loopback-only browser fixture.

Session/env saves use the real API. Keychain storage is stubbed at the API
boundary so this request-payload regression can run outside macOS.
"""
import argparse
import ipaddress
import json
import uuid
from urllib.parse import urlparse

from playwright.sync_api import sync_playwright, expect


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--fixture-file', required=True)
    parser.add_argument('--chromium', required=True)
    args = parser.parse_args()
    with open(args.fixture_file, encoding='utf-8') as file:
        fixture = json.load(file)
    host = urlparse(fixture['url']).hostname
    if host != 'localhost' and not ipaddress.ip_address(host).is_loopback:
        raise ValueError('Tests require a loopback-only fixture')
    with sync_playwright() as playwright:
        browser = playwright.chromium.launch(executable_path=args.chromium, headless=True)
        page = browser.new_page()
        errors, writes, failures, created = [], [], [], []
        prefix = f'Credential {uuid.uuid4().hex[:8]}'
        page.on('pageerror', lambda error: errors.append(str(error)))

        def profile_write(route):
            if route.request.method != 'POST':
                return route.continue_()
            body = route.request.post_data_json
            writes.append(body)
            if body['secretMode'] == 'keychain':
                # Do not use a real OS keychain in a synthetic browser test.
                profile = {key: value for key, value in body.items() if key != 'secret'}
                profile['id'] = 'synthetic-keychain-profile'
                return route.fulfill(status=200, json=profile)
            return route.continue_()

        page.route('**/api/profiles', profile_write)
        page.goto(fixture['url'])
        page.locator('#source-profile option').nth(1).wait_for(state='attached')
        session = page.request.get(fixture['url'].split('/?')[0] + '/api/session').json()
        credential_storage = session.get('credentialStorage', {})
        keyring_option = page.locator('[name="secretMode"] option[value="keychain"]')
        if credential_storage.get('supported'):
            if credential_storage.get('provider') not in keyring_option.text_content() or keyring_option.is_disabled():
                failures.append('credential-vault label does not match the supported app server platform')
            modes = ('session', 'keychain', 'env')
        else:
            if not keyring_option.is_disabled() or 'unsupported' not in keyring_option.text_content().lower():
                failures.append('unsupported server platform left OS credential storage selectable')
            modes = ('session', 'env')
        for mode in modes:
            page.locator('#profiles-open').click()
            form = page.locator('#profiles-form')
            for key, value in {
                'name': f'{prefix} mode {mode} fixture',
                'environmentId': 'synthetic-env',
                'authEnvironmentId': 'synthetic-auth',
                'clientId': 'synthetic-client',
            }.items():
                form.locator(f'[name="{key}"]').fill(value)
            # Exercise both directions with a stale value in the hidden control.
            form.locator('[name="secret"]').fill('synthetic-session-credential')
            form.locator('[name="secretMode"]').select_option('env')
            form.locator('[name="secretEnv"]').fill('SYNTHETIC_PROFILE_SECRET')
            form.locator('[name="secretMode"]').select_option(mode)
            if mode == 'keychain':
                help_text = page.locator('#secret-storage-help').inner_text()
                if credential_storage.get('provider', '') not in help_text or 'click Save profile' not in help_text:
                    failures.append('keychain mode does not explain how the entered secret is stored')
                if credential_storage.get('provider') == 'Linux Secret Service' and credential_storage.get('availability') != 'not_probed':
                    failures.append('Linux Secret Service was claimed available before use')
            with page.expect_response(lambda response: response.url.endswith('/api/profiles') and response.request.method == 'POST') as saved:
                page.locator('#profile-save').click()
            body = writes[-1]
            if mode != 'env' and body.get('secretEnv'):
                failures.append(f'{mode}: inactive environment-variable field was submitted')
            if mode == 'env' and body.get('secret'):
                failures.append('env: inactive direct secret was submitted')
            if mode != 'env' and body.get('secret') != 'synthetic-session-credential':
                failures.append(f'{mode}: active direct secret was lost')
            if saved.value.status != 200:
                failures.append(f'{mode}: profile save returned HTTP {saved.value.status}')
            elif mode != 'keychain':
                created.append(saved.value.json()['id'])
            page.get_by_role('button', name='Close environment profiles', exact=True).click()

        if credential_storage.get('supported'):
            # Switching away from an OS vault must tell the user the old entry
            # is removed and require a new direct secret for Session only.
            page.locator('#profiles-open').click()
            page.locator('.profile-list-item').filter(has_text=f'{prefix} mode keychain fixture').get_by_role('button', name='Edit', exact=True).click()
            form = page.locator('#profiles-form')
            form.locator('[name="secretMode"]').select_option('session')
            if not form.locator('[name="secret"]').evaluate('(node) => node.required'):
                failures.append('switching from keyring to session did not require a new secret')
            if 'removes the old vault copy' not in page.locator('#secret-storage-help').inner_text():
                failures.append('switching away from keyring did not explain that its old copy is removed')
            form.locator('[name="secret"]').fill('synthetic-session-after-keyring')
            with page.expect_response(lambda response: response.url.endswith('/api/profiles') and response.request.method == 'POST') as migrated:
                page.locator('#profile-save').click()
            if migrated.value.status == 200:
                created.append(migrated.value.json()['id'])
            else:
                failures.append('keyring-to-session save failed')

        # Editing an existing env profile must not carry its variable name into
        # the new session-mode payload or persisted profile metadata.
        page.locator('#profiles-open').click()
        page.locator('.profile-list-item').filter(has_text=f'{prefix} mode env fixture').get_by_role('button', name='Edit', exact=True).click()
        form = page.locator('#profiles-form')
        form.locator('[name="secretMode"]').select_option('session')
        form.locator('[name="secret"]').fill('synthetic-replacement-credential')
        with page.expect_response(lambda response: response.url.endswith('/api/profiles') and response.request.method == 'POST') as edited:
            page.locator('#profile-save').click()
        if writes[-1].get('secretEnv') or edited.value.status != 200:
            failures.append('edit env to session: stale variable prevented save')
        else:
            expect(page.locator('#profile-error')).to_be_hidden()
            profile = edited.value.json()
            assert profile['secretMode'] == 'session' and not profile.get('secretEnv')
        base = fixture['url'].split('/?')[0]
        csrf = page.request.get(base + '/api/session').json()['csrf']
        for profile_id in created:
            deleted = page.request.delete(base + '/api/profiles/' + profile_id, headers={'X-CSRF-Token': csrf})
            assert deleted.status == 200, 'synthetic profile cleanup failed'
        browser.close()
        assert not errors, errors
        assert not failures, '; '.join(failures)
        print('PASS: supported credential modes and profile edits omit inactive credential fields')


if __name__ == '__main__':
    main()
