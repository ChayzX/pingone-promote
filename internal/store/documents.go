package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Document kinds kept by the Terraform authoring workflow. They hold
// metadata only (selections, decisions, IDs and repository coordinates),
// never secret values or inventory content.
const (
	KindDrafts      = "drafts"
	KindSubmissions = "submissions"
)

var documentIDRE = regexp.MustCompile(`^[A-Za-z0-9_-]{8,80}$`)

func documentName(kind, id string) (string, error) {
	if kind != KindDrafts && kind != KindSubmissions {
		return "", errors.New("unknown document kind")
	}
	if !documentIDRE.MatchString(id) {
		return "", errors.New("invalid document ID")
	}
	return filepath.Join(kind, id+".json"), nil
}

func (s *Store) ensureKindDir(kind string) error {
	dir := s.path(kind)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return persistenceError(fmt.Errorf("create %s directory: %w", kind, err))
	}
	if err := os.Chmod(dir, 0700); err != nil {
		return persistenceError(fmt.Errorf("secure %s directory: %w", kind, err))
	}
	return nil
}

// SaveDocument atomically writes one metadata document.
func (s *Store) SaveDocument(kind, id string, v any) error {
	name, err := documentName(kind, id)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureKindDir(kind); err != nil {
		return err
	}
	return s.write(name, v)
}

// LoadDocument reads one metadata document strictly. A missing document
// returns ErrRecordNotFound.
func (s *Store) LoadDocument(kind, id string, out any) error {
	name, err := documentName(kind, id)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := s.read(name)
	if errors.Is(err, os.ErrNotExist) {
		return ErrRecordNotFound
	}
	if err != nil {
		return err
	}
	return decode(name, b, out)
}

// ListDocuments returns the IDs of a kind, sorted.
func (s *Store) ListDocuments(kind string) ([]string, error) {
	if kind != KindDrafts && kind != KindSubmissions {
		return nil, errors.New("unknown document kind")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := os.ReadDir(s.path(kind))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, persistenceError(err)
	}
	var ids []string
	for _, e := range entries {
		id, ok := strings.CutSuffix(e.Name(), ".json")
		if ok && e.Type().IsRegular() && documentIDRE.MatchString(id) {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids, nil
}
