package store

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"pingone-promote/internal/pingone"
)

type fakeCredentialProvider struct {
	values       map[string]string
	setErr       error
	getErr       error
	delErr       error
	beforeSet    func() error
	beforeDelete func() error
}

func (f *fakeCredentialProvider) Set(id, secret string) error {
	if f.setErr != nil {
		return f.setErr
	}
	if f.values == nil {
		f.values = map[string]string{}
	}
	f.values[id] = secret
	if f.beforeSet != nil {
		return f.beforeSet()
	}
	return nil
}
func (f *fakeCredentialProvider) Get(id string) (string, error) {
	if f.getErr != nil {
		return "", f.getErr
	}
	value, ok := f.values[id]
	if !ok {
		return "", errCredentialNotFound
	}
	return value, nil
}
func (f *fakeCredentialProvider) Delete(id string) error {
	if f.delErr != nil {
		return f.delErr
	}
	delete(f.values, id)
	if f.beforeDelete != nil {
		return f.beforeDelete()
	}
	return nil
}

func blockProfilesWrite(t *testing.T, dir string) func() {
	t.Helper()
	path := filepath.Join(dir, "profiles.json")
	backup := filepath.Join(dir, "profiles.json.backup")
	if err := os.Rename(path, backup); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		_ = os.Rename(backup, path)
		t.Fatal(err)
	}
	return func() {
		if err := os.Remove(path); err != nil {
			t.Errorf("remove synthetic profiles directory: %v", err)
		}
		if err := os.Rename(backup, path); err != nil {
			t.Errorf("restore profiles.json after synthetic failure: %v", err)
		}
	}
}

func validProfile() pingone.Profile {
	return pingone.Profile{Name: "Production", Region: "us", EnvironmentID: "env_123", ClientID: "worker-1", SecretMode: "session"}
}

func TestSessionSecretIsMemoryOnlyAndNotRestored(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.SaveProfile(validProfile(), "top-secret")
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Secret(p)
	if err != nil || got != "top-secret" {
		t.Fatalf("Secret() = %q, %v", got, err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "profiles.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "top-secret") {
		t.Fatal("session secret was persisted")
	}
	var persisted any
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatal(err)
	}
	profiles, err := s.ListProfiles()
	if err != nil || len(profiles) != 1 {
		t.Fatalf("ListProfiles() = %v, %v", profiles, err)
	}
	if strings.Contains(string(data), "secret") && strings.Contains(string(data), `"secret":`) {
		t.Fatal("secret field was persisted")
	}
	s2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s2.Secret(p); err == nil {
		t.Fatal("session secret unexpectedly survived reopen")
	}
}

func TestEnvironmentSecretIsResolvedAtUseTime(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	p := validProfile()
	p.SecretMode, p.SecretEnv = "env", "PINGONE_TEST_SECRET"
	p, err = s.SaveProfile(p, "ignored")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(p.SecretEnv, "first")
	if got, err := s.Secret(p); err != nil || got != "first" {
		t.Fatalf("Secret() = %q, %v", got, err)
	}
	t.Setenv(p.SecretEnv, "second")
	if got, err := s.Secret(p); err != nil || got != "second" {
		t.Fatalf("Secret() = %q, %v", got, err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "profiles.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "first") || strings.Contains(string(data), "second") {
		t.Fatal("environment secret value was persisted")
	}
}

func TestKeyringCredentialUsesProviderAndKeepsFilesSecretFree(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	provider := &fakeCredentialProvider{}
	s.credentials = provider
	p := validProfile()
	p.SecretMode = "keychain"
	p, err = s.SaveProfile(p, "synthetic-keyring-secret")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := s.Secret(p); err != nil || got != "synthetic-keyring-secret" {
		t.Fatalf("Secret() = %q, %v", got, err)
	}
	for _, filename := range []string{"profiles.json", "records.json"} {
		data, err := os.ReadFile(filepath.Join(dir, filename))
		if err == nil && strings.Contains(string(data), "synthetic-keyring-secret") {
			t.Fatalf("credential persisted in %s", filename)
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
	}
	if err := s.DeleteProfile(p.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := provider.values[p.ID]; ok {
		t.Fatal("profile deletion did not remove keyring credential")
	}
}

func TestNewKeyringProfileRequiresASecret(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s.credentials = &fakeCredentialProvider{}
	p := validProfile()
	p.SecretMode = "keychain"
	if _, err := s.SaveProfile(p, ""); err == nil || !strings.Contains(err.Error(), "enter the PingOne client secret") {
		t.Fatalf("SaveProfile error = %v, want a prompt to enter the secret", err)
	}
	profiles, err := s.ListProfiles()
	if err != nil {
		t.Fatal(err)
	}
	if len(profiles) != 0 {
		t.Fatalf("profile without a keyring credential was persisted: %+v", profiles)
	}
}

func TestExistingKeyringProfileBlankSecretPreservesStoredCredential(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	provider := &fakeCredentialProvider{}
	s.credentials = provider
	p := validProfile()
	p.SecretMode = "keychain"
	p, err = s.SaveProfile(p, "stored-secret")
	if err != nil {
		t.Fatal(err)
	}
	p.Name = "Renamed profile"
	if _, err := s.SaveProfile(p, ""); err != nil {
		t.Fatal(err)
	}
	if provider.values[p.ID] != "stored-secret" {
		t.Fatal("blank-secret edit changed the existing keyring credential")
	}
}

func TestKeyringUnavailableErrorDoesNotPersistProfileOrSecret(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	provider := &fakeCredentialProvider{setErr: errors.New("vault unavailable")}
	s.credentials = provider
	p := validProfile()
	p.SecretMode = "keychain"
	if _, err := s.SaveProfile(p, "synthetic-secret"); err == nil || !strings.Contains(err.Error(), "OS keyring") {
		t.Fatalf("SaveProfile error = %v, want explicit keyring failure", err)
	}
	profiles, err := s.ListProfiles()
	if err != nil {
		t.Fatal(err)
	}
	if len(profiles) != 0 {
		t.Fatalf("failed keyring save persisted profile: %+v", profiles)
	}
}

func TestSaveProfileRestoresKeyringCredentialWhenMetadataWriteFails(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	provider := &fakeCredentialProvider{}
	s.credentials = provider
	p := validProfile()
	p.SecretMode = "keychain"
	p, err = s.SaveProfile(p, "prior-secret")
	if err != nil {
		t.Fatal(err)
	}
	var restore func()
	provider.beforeSet = func() error {
		provider.beforeSet = nil
		restore = blockProfilesWrite(t, dir)
		return nil
	}
	updated := p
	updated.Name = "Renamed profile"
	_, saveErr := s.SaveProfile(updated, "replacement-secret")
	if restore != nil {
		restore()
	}
	if saveErr == nil {
		t.Fatal("SaveProfile unexpectedly succeeded with profiles.json replaced by a directory")
	}
	if got := provider.values[p.ID]; got != "prior-secret" {
		t.Fatalf("credential after failed profile update = %q, want prior credential", got)
	}
	profiles, err := s.ListProfiles()
	if err != nil {
		t.Fatal(err)
	}
	if len(profiles) != 1 || profiles[0].Name != p.Name {
		t.Fatalf("profile metadata after failed update = %+v", profiles)
	}
	if strings.Contains(string(mustRead(t, filepath.Join(dir, "profiles.json"))), "prior-secret") || strings.Contains(string(mustRead(t, filepath.Join(dir, "profiles.json"))), "replacement-secret") {
		t.Fatal("credential was written to profiles.json")
	}
}

func TestDeleteProfileRestoresKeyringCredentialWhenMetadataWriteFails(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	provider := &fakeCredentialProvider{}
	s.credentials = provider
	p := validProfile()
	p.SecretMode = "keychain"
	p, err = s.SaveProfile(p, "retained-secret")
	if err != nil {
		t.Fatal(err)
	}
	var restore func()
	provider.beforeDelete = func() error {
		provider.beforeDelete = nil
		restore = blockProfilesWrite(t, dir)
		return nil
	}
	deleteErr := s.DeleteProfile(p.ID)
	if restore != nil {
		restore()
	}
	if deleteErr == nil {
		t.Fatal("DeleteProfile unexpectedly succeeded with profiles.json replaced by a directory")
	}
	if got, ok := provider.values[p.ID]; !ok || got != "retained-secret" {
		t.Fatalf("credential after failed profile deletion = %q, present %v", got, ok)
	}
	profiles, err := s.ListProfiles()
	if err != nil {
		t.Fatal(err)
	}
	if len(profiles) != 1 || profiles[0].ID != p.ID {
		t.Fatalf("profile metadata after failed deletion = %+v", profiles)
	}
	if strings.Contains(string(mustRead(t, filepath.Join(dir, "profiles.json"))), "retained-secret") {
		t.Fatal("credential was written to profiles.json")
	}
}

func TestSaveProfileReportsCompensationFailureWithoutCredentialValues(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	provider := &fakeCredentialProvider{}
	s.credentials = provider
	p := validProfile()
	p.SecretMode = "keychain"
	p, err = s.SaveProfile(p, "prior-secret-canary")
	if err != nil {
		t.Fatal(err)
	}
	var restore func()
	provider.beforeSet = func() error {
		provider.beforeSet = nil
		provider.setErr = errors.New("provider diagnostic includes secret canaries")
		restore = blockProfilesWrite(t, dir)
		return nil
	}
	updated := p
	updated.Name = "Rename that cannot be saved"
	_, saveErr := s.SaveProfile(updated, "replacement-secret-canary")
	if restore != nil {
		restore()
	}
	if saveErr == nil || !strings.Contains(saveErr.Error(), "state may be inconsistent") {
		t.Fatalf("SaveProfile error = %v, want explicit compensation failure", saveErr)
	}
	if strings.Contains(saveErr.Error(), "prior-secret-canary") || strings.Contains(saveErr.Error(), "replacement-secret-canary") || strings.Contains(saveErr.Error(), "provider diagnostic") {
		t.Fatalf("credential data leaked through error: %v", saveErr)
	}
}

func TestDeleteProfileReportsCompensationFailureWithoutCredentialValues(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	provider := &fakeCredentialProvider{}
	s.credentials = provider
	p := validProfile()
	p.SecretMode = "keychain"
	p, err = s.SaveProfile(p, "delete-secret-canary")
	if err != nil {
		t.Fatal(err)
	}
	var restore func()
	provider.beforeDelete = func() error {
		provider.beforeDelete = nil
		provider.setErr = errors.New("provider diagnostic includes secret canary")
		restore = blockProfilesWrite(t, dir)
		return nil
	}
	deleteErr := s.DeleteProfile(p.ID)
	if restore != nil {
		restore()
	}
	if deleteErr == nil || !strings.Contains(deleteErr.Error(), "state may be inconsistent") {
		t.Fatalf("DeleteProfile error = %v, want explicit compensation failure", deleteErr)
	}
	if strings.Contains(deleteErr.Error(), "delete-secret-canary") || strings.Contains(deleteErr.Error(), "provider diagnostic") {
		t.Fatalf("credential data leaked through error: %v", deleteErr)
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestSaveProfileUpsertsAndPreservesID(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.SaveProfile(validProfile(), "secret")
	if err != nil {
		t.Fatal(err)
	}
	updated := validProfile()
	updated.ID, updated.Name = p.ID, "Renamed"
	updated, err = s.SaveProfile(updated, "")
	if err != nil {
		t.Fatal(err)
	}
	if updated.ID != p.ID || updated.Name != "Renamed" {
		t.Fatalf("updated profile = %+v", updated)
	}
	if got, err := s.Secret(updated); err != nil || got != "secret" {
		t.Fatalf("preserved secret = %q, %v", got, err)
	}
	profiles, err := s.ListProfiles()
	if err != nil || len(profiles) != 1 {
		t.Fatalf("profiles = %v, %v", profiles, err)
	}
}

func TestSaveProfileValidation(t *testing.T) {
	tests := []struct {
		name string
		edit func(*pingone.Profile)
	}{
		{"missing name", func(p *pingone.Profile) { p.Name = "" }},
		{"unknown region", func(p *pingone.Profile) { p.Region = "moon" }},
		{"bad environment", func(p *pingone.Profile) { p.EnvironmentID = "../bad" }},
		{"bad client", func(p *pingone.Profile) { p.ClientID = "client with spaces" }},
		{"bad auth environment", func(p *pingone.Profile) { p.AuthEnvironmentID = "!" }},
		{"unknown secret mode", func(p *pingone.Profile) { p.SecretMode = "file" }},
		{"bad env variable", func(p *pingone.Profile) { p.SecretMode, p.SecretEnv = "env", "1bad" }},
		{"env mode without variable", func(p *pingone.Profile) { p.SecretMode = "env" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, err := Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			p := validProfile()
			tt.edit(&p)
			if _, err := s.SaveProfile(p, ""); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestAtomicFilesArePrivateAndCorruptionIsNotOverwritten(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveProfile(validProfile(), ""); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveRecord(Record{ID: "r1", Kind: "snapshot"}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"profiles.json", "records.json"} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0600 {
			t.Errorf("%s mode = %o", name, info.Mode().Perm())
		}
	}
	if info, err := os.Stat(dir); err != nil || info.Mode().Perm() != 0700 {
		t.Fatalf("directory mode = %v, %v", info, err)
	}
	bad := []byte("not json")
	if err := os.WriteFile(filepath.Join(dir, "profiles.json"), bad, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(dir); err == nil {
		t.Fatal("expected Open to reject corrupted file")
	}
	got, err := os.ReadFile(filepath.Join(dir, "profiles.json"))
	if err != nil || string(got) != string(bad) {
		t.Fatalf("corrupt file changed: %q, %v", got, err)
	}
}

func TestRecordMetadataSurvivesReopenWithoutPayload(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	r := Record{ID: "local-1", NativeID: "native-2", Kind: "promotion", ParentID: "parent", Description: "release", Status: "READY", Intent: "deploy", Source: validProfile(), Target: pingone.Profile{ID: "target", Name: "Staging"}, Selected: []string{"resource-1"}, Mappings: map[string]string{"resource-1": "resource-2"}, Resources: []ResourceRef{{ID: "resource-1", URL: "https://example.test/r/1", Name: "Flow", Type: "flow"}}, CreatedAt: "now", UpdatedAt: "later"}
	if err := s.SaveRecord(r); err != nil {
		t.Fatal(err)
	}
	s2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s2.GetRecord(r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != r.ID || got.NativeID != r.NativeID || got.Source.Name != r.Source.Name || got.Resources[0].Type != "flow" {
		t.Fatalf("record = %+v", got)
	}
	data, err := os.ReadFile(filepath.Join(dir, "records.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"configuration", "payload", "token", "top-secret"} {
		if strings.Contains(strings.ToLower(string(data)), forbidden) {
			t.Fatalf("history contains %q: %s", forbidden, data)
		}
	}
}

func TestDeleteProfileAndConcurrentRecordSaves(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.SaveProfile(validProfile(), "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteProfile(p.ID); err != nil {
		t.Fatal(err)
	}
	profiles, err := s.ListProfiles()
	if err != nil || len(profiles) != 0 {
		t.Fatalf("profiles = %v, %v", profiles, err)
	}
	const n = 32
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := "record-" + string(rune('A'+i))
			if err := s.SaveRecord(Record{ID: id, Kind: "test"}); err != nil {
				t.Errorf("SaveRecord: %v", err)
			}
		}(i)
	}
	wg.Wait()
	records, err := s.ListRecords()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != n {
		t.Fatalf("got %d records, want %d", len(records), n)
	}
}

func TestGetMissingRecord(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.GetRecord("missing")
	if err == nil || !errors.Is(err, ErrRecordNotFound) || errors.Is(err, os.ErrNotExist) {
		t.Fatalf("GetRecord error = %v", err)
	}
}

func TestSaveProfileRejectsOversizedAndControlCharacterNames(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for name, bad := range map[string]string{"too long": strings.Repeat("A", 81), "newline": "Prod\nDest", "tab": "Prod\tDest", "nul": "Prod\x00"} {
		p := validProfile()
		p.Name = bad
		if _, err := s.SaveProfile(p, "secret"); err == nil {
			t.Errorf("%s name was accepted", name)
		}
	}
	p := validProfile()
	p.Name = "  " + strings.Repeat("é", 80) + "  "
	saved, err := s.SaveProfile(p, "secret")
	if err != nil {
		t.Fatalf("80-character name rejected: %v", err)
	}
	if saved.Name != strings.Repeat("é", 80) {
		t.Fatalf("name was not trimmed: %q", saved.Name)
	}
	if profiles, _ := s.ListProfiles(); len(profiles) != 1 {
		t.Fatalf("rejected profiles were persisted: %v", profiles)
	}
}
