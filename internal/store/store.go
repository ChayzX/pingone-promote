// Package store provides private local persistence for reusable profiles and
// metadata-only operation history.
package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"pingone-promote/internal/pingone"
)

// ResourceRef is the safe, metadata-only description of a PingOne resource.
type ResourceRef struct {
	ID   string `json:"id"`
	URL  string `json:"url"`
	Name string `json:"name"`
	Type string `json:"type"`
}

// Record contains durable metadata about an inventory, promotion, or rollback
// operation. Payloads, configurations, credentials, and tokens are excluded.
type Record struct {
	ID                   string            `json:"id"`
	NativeID             string            `json:"nativeId"`
	Kind                 string            `json:"kind"`
	ParentID             string            `json:"parentId"`
	Description          string            `json:"description"`
	Status               string            `json:"status"`
	Intent               string            `json:"intent"`
	Source               pingone.Profile   `json:"source"`
	Target               pingone.Profile   `json:"target"`
	SourceSnapshotID     string            `json:"sourceSnapshotId"`
	TargetSnapshotID     string            `json:"targetSnapshotId"`
	SourceOrganizationID string            `json:"sourceOrganizationId"`
	TargetOrganizationID string            `json:"targetOrganizationId"`
	SourceAPIOrigin      string            `json:"sourceApiOrigin"`
	TargetAPIOrigin      string            `json:"targetApiOrigin"`
	Selected             []string          `json:"selected"`
	Mappings             map[string]string `json:"mappings"`
	Resources            []ResourceRef     `json:"resources"`
	CreatedAt            string            `json:"createdAt"`
	UpdatedAt            string            `json:"updatedAt"`
}

// Store persists profiles and history under dir. Session secrets remain only
// in this Store's memory.
type Store struct {
	dir         string
	mu          sync.Mutex
	session     map[string]string
	credentials credentialProvider
}

type credentialProvider interface {
	Set(id, secret string) error
	Get(id string) (string, error)
	Delete(id string) error
}

type platformCredentialProvider struct{}

func (platformCredentialProvider) Set(id, secret string) error   { return keychainSet(id, secret) }
func (platformCredentialProvider) Get(id string) (string, error) { return keychainGet(id) }
func (platformCredentialProvider) Delete(id string) error        { return keychainDelete(id) }

func credentialBeforeChange(provider credentialProvider, id string) (string, bool, error) {
	secret, err := provider.Get(id)
	if errors.Is(err, errCredentialNotFound) {
		return "", false, nil
	}
	if err != nil {
		return "", false, safeCredentialProviderFailure("OS credential vault", "verify", "Unlock the OS credential vault and retry.", err)
	}
	return secret, true, nil
}

func restoreCredential(provider credentialProvider, id, secret string, existed bool) error {
	if existed {
		return provider.Set(id, secret)
	}
	return provider.Delete(id)
}

func compensationFailure(err error, action string) error {
	if err == nil {
		return errors.New(action)
	}
	return fmt.Errorf("%s: %w", action, ErrCredentialStateInconsistent)
}

// PersistenceError marks a local storage or stored-data error for API callers.
// Its original cause remains available through Unwrap for local diagnostics.
type PersistenceError struct{ err error }

func (e *PersistenceError) Error() string { return e.err.Error() }
func (e *PersistenceError) Unwrap() error { return e.err }

func persistenceError(err error) error {
	if err == nil {
		return nil
	}
	return &PersistenceError{err: err}
}

// ErrRecordNotFound identifies an absent record without masking store read failures.
var ErrRecordNotFound = errors.New("record not found")

// ErrCredentialStateInconsistent indicates that a failed metadata write could
// not be compensated in the OS keyring.
var ErrCredentialStateInconsistent = errors.New("profile metadata and OS keyring state may be inconsistent; inspect both before continuing")

// Open opens or creates a private store directory and rejects malformed
// existing data files instead of silently replacing them.
func Open(dir string) (*Store, error) {
	if dir == "" {
		return nil, errors.New("store directory is required")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("create store directory: %w", err)
	}
	if err := os.Chmod(dir, 0700); err != nil {
		return nil, fmt.Errorf("secure store directory: %w", err)
	}
	s := &Store{dir: dir, session: make(map[string]string), credentials: platformCredentialProvider{}}
	if _, err := s.profiles(); err != nil {
		return nil, err
	}
	if _, err := s.records(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) path(name string) string { return filepath.Join(s.dir, name) }

func (s *Store) read(name string) ([]byte, error) {
	b, err := os.ReadFile(s.path(name))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		return nil, persistenceError(err)
	}
	if err := os.Chmod(s.path(name), 0600); err != nil {
		return nil, persistenceError(fmt.Errorf("secure %s: %w", name, err))
	}
	return b, nil
}

func decode(name string, b []byte, out any) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		return persistenceError(fmt.Errorf("decode %s: %w", name, err))
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return persistenceError(fmt.Errorf("decode %s: trailing JSON data", name))
	}
	return nil
}

func (s *Store) write(name string, value any) error {
	b, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return persistenceError(fmt.Errorf("encode %s: %w", name, err))
	}
	b = append(b, '\n')
	f, err := os.CreateTemp(s.dir, ".store-*.tmp")
	if err != nil {
		return persistenceError(fmt.Errorf("create temporary %s: %w", name, err))
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err := f.Chmod(0600); err != nil {
		f.Close()
		return persistenceError(fmt.Errorf("secure temporary %s: %w", name, err))
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		return persistenceError(fmt.Errorf("write %s: %w", name, err))
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return persistenceError(fmt.Errorf("sync %s: %w", name, err))
	}
	if err := f.Close(); err != nil {
		return persistenceError(fmt.Errorf("close %s: %w", name, err))
	}
	if err := os.Rename(tmp, s.path(name)); err != nil {
		return persistenceError(fmt.Errorf("replace %s: %w", name, err))
	}
	if d, err := os.Open(s.dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

func (s *Store) profiles() ([]pingone.Profile, error) {
	b, err := s.read("profiles.json")
	if errors.Is(err, os.ErrNotExist) {
		return []pingone.Profile{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read profiles: %w", err)
	}
	var v []pingone.Profile
	if err := decode("profiles.json", b, &v); err != nil {
		return nil, err
	}
	if v == nil {
		v = []pingone.Profile{}
	}
	return v, nil
}

// ListProfiles returns profiles without any secret material.
func (s *Store) ListProfiles() ([]pingone.Profile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.profiles()
}

// SaveProfile validates and creates or updates a profile. For session and
// Keychain modes, secret is stored in the selected credential provider. Passing
// an empty secret on an update preserves any existing credential.
func (s *Store) SaveProfile(p pingone.Profile, secret string) (pingone.Profile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p.ID == "" {
		id, err := randomID()
		if err != nil {
			return p, err
		}
		p.ID = id
	}
	p.Name = strings.TrimSpace(p.Name)
	if err := validateProfileName(p.Name); err != nil {
		return p, err
	}
	if err := validateProfile(p); err != nil {
		return p, err
	}
	profiles, err := s.profiles()
	if err != nil {
		return p, err
	}
	var existingMode string
	profileExists := false
	for _, existing := range profiles {
		if existing.ID == p.ID {
			existingMode = existing.SecretMode
			profileExists = true
			break
		}
	}
	oldSecret, hasOldSecret := s.session[p.ID]
	var priorCredential string
	var hadPriorCredential bool
	changesCredential := p.SecretMode == "keychain" && secret != ""
	removesCredential := profileExists && existingMode == "keychain" && p.SecretMode != "keychain"
	if removesCredential && p.SecretMode == "session" && secret == "" {
		return p, errors.New("enter the PingOne client secret for the new session before switching away from the OS credential vault")
	}
	if p.SecretMode == "keychain" && secret == "" {
		if !profileExists || existingMode != "keychain" {
			return p, errors.New("enter the PingOne client secret before saving this profile to the OS credential vault")
		}
		_, hadPriorCredential, err = credentialBeforeChange(s.credentials, p.ID)
		if err != nil {
			return p, err
		}
		if !hadPriorCredential {
			return p, errors.New("no OS credential is stored for this profile; enter the PingOne client secret and save again")
		}
	}
	if changesCredential {
		priorCredential, hadPriorCredential, err = credentialBeforeChange(s.credentials, p.ID)
		if err != nil {
			return p, err
		}
		if err := s.credentials.Set(p.ID, secret); err != nil {
			return p, compensationFailure(restoreCredential(s.credentials, p.ID, priorCredential, hadPriorCredential), "could not store credential in the OS keyring")
		}
	} else if removesCredential {
		priorCredential, hadPriorCredential, err = credentialBeforeChange(s.credentials, p.ID)
		if err != nil {
			return p, err
		}
		if err := s.credentials.Delete(p.ID); err != nil {
			return p, compensationFailure(restoreCredential(s.credentials, p.ID, priorCredential, hadPriorCredential), "could not remove the previous OS keyring credential")
		}
	}
	found := false
	for i := range profiles {
		if profiles[i].ID == p.ID {
			profiles[i] = p
			found = true
			break
		}
	}
	if !found {
		profiles = append(profiles, p)
	}
	if err := s.write("profiles.json", profiles); err != nil {
		if changesCredential || removesCredential {
			if restoreErr := restoreCredential(s.credentials, p.ID, priorCredential, hadPriorCredential); restoreErr != nil {
				return p, errors.Join(err, compensationFailure(restoreErr, "profile metadata was not saved"))
			}
		}
		return p, err
	}
	if p.SecretMode == "session" {
		if secret != "" {
			s.session[p.ID] = secret
		} else if hasOldSecret {
			s.session[p.ID] = oldSecret
		}
	} else {
		delete(s.session, p.ID)
	}
	return p, nil
}

// DeleteProfile removes a profile and its in-memory session credential.
func (s *Store) DeleteProfile(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	profiles, err := s.profiles()
	if err != nil {
		return err
	}
	found := false
	var deleted *pingone.Profile
	kept := make([]pingone.Profile, 0, len(profiles))
	for _, p := range profiles {
		if p.ID == id {
			found = true
			copy := p
			deleted = &copy
			continue
		}
		kept = append(kept, p)
	}
	if !found {
		return fmt.Errorf("profile %q not found", id)
	}
	var priorCredential string
	var hadPriorCredential bool
	changesCredential := deleted.SecretMode == "keychain"
	if changesCredential {
		priorCredential, hadPriorCredential, err = credentialBeforeChange(s.credentials, id)
		if err != nil {
			return err
		}
		if err := s.credentials.Delete(id); err != nil {
			return compensationFailure(restoreCredential(s.credentials, id, priorCredential, hadPriorCredential), "could not delete credential from the OS keyring")
		}
	}
	if err := s.write("profiles.json", kept); err != nil {
		if changesCredential {
			if restoreErr := restoreCredential(s.credentials, id, priorCredential, hadPriorCredential); restoreErr != nil {
				return errors.Join(err, compensationFailure(restoreErr, "profile metadata was not deleted"))
			}
		}
		return err
	}
	delete(s.session, id)
	return nil
}

// Secret returns a profile's credential from its configured provider.
func (s *Store) Secret(p pingone.Profile) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := validateProfile(p); err != nil {
		return "", err
	}
	switch p.SecretMode {
	case "session":
		v, ok := s.session[p.ID]
		if !ok || v == "" {
			return "", fmt.Errorf("no session secret for profile %q", p.ID)
		}
		return v, nil
	case "env":
		v := os.Getenv(p.SecretEnv)
		if v == "" {
			return "", fmt.Errorf("environment variable %s is empty", p.SecretEnv)
		}
		return v, nil
	case "keychain":
		secret, err := s.credentials.Get(p.ID)
		if errors.Is(err, errCredentialNotFound) {
			return "", fmt.Errorf("no OS credential is stored for profile %q; edit the profile, enter the PingOne client secret, and save", p.ID)
		}
		if err != nil {
			return "", safeCredentialProviderFailure("OS credential vault", "access", "Unlock the OS credential vault and retry.", err)
		}
		return secret, nil
	default:
		return "", fmt.Errorf("unsupported secret mode %q", p.SecretMode)
	}
}

func (s *Store) records() ([]Record, error) {
	b, err := s.read("records.json")
	if errors.Is(err, os.ErrNotExist) {
		return []Record{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read records: %w", err)
	}
	var v []Record
	if err := decode("records.json", b, &v); err != nil {
		return nil, err
	}
	if v == nil {
		v = []Record{}
	}
	return v, nil
}

// ListRecords returns the durable metadata history.
func (s *Store) ListRecords() ([]Record, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.records() }

// SaveRecord inserts or updates a record by its local ID. The typed schema
// means arbitrary API payload or configuration fields are never serialized.
func (s *Store) SaveRecord(r Record) error {
	if r.ID == "" {
		return errors.New("record ID is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	records, err := s.records()
	if err != nil {
		return err
	}
	found := false
	for i := range records {
		if records[i].ID == r.ID {
			// These fields identify the original operation and are filled once.
			if r.NativeID == "" {
				r.NativeID = records[i].NativeID
			}
			if r.Kind == "" {
				r.Kind = records[i].Kind
			}
			if r.ParentID == "" {
				r.ParentID = records[i].ParentID
			}
			if r.CreatedAt == "" {
				r.CreatedAt = records[i].CreatedAt
			}
			if r.Source.ID == "" {
				r.Source = records[i].Source
			}
			if r.Target.ID == "" {
				r.Target = records[i].Target
			}
			records[i] = r
			found = true
			break
		}
	}
	if !found {
		records = append(records, r)
	}
	return s.write("records.json", records)
}

// GetRecord loads one history entry by local ID.
func (s *Store) GetRecord(id string) (Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	records, err := s.records()
	if err != nil {
		return Record{}, err
	}
	for _, r := range records {
		if r.ID == id {
			return r, nil
		}
	}
	return Record{}, fmt.Errorf("record %q: %w", id, ErrRecordNotFound)
}

// Directory is the private application-data directory used by additional
// metadata-only adapters. Credential values must not be written there.
func (s *Store) Directory() string { return s.dir }
