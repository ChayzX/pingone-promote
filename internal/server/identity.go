package server

import (
	"errors"
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Person is who a request is recorded as coming from.
type Person struct {
	Name     string
	Verified bool
}

// Recorded is the requestedBy text: the name, marked "(unverified)" unless
// a sign-in verified it.
func (p Person) Recorded() string {
	if p.Verified {
		return p.Name
	}
	return p.Name + " (unverified)"
}

// Identity tells handlers who is making a request. Handlers depend only on
// this interface, so a sign-in (OIDC, reserved as signIn "pingone") can
// replace TypedName without changing them. A typed name confers no
// authorization and no draft ownership: drafts belong to the browser
// session.
type Identity interface {
	Who(r *http.Request) (Person, error)
}

// TypedName is phase 1: the person types their name on the form and it is
// recorded as unverified.
type TypedName struct {
	// Field is the form field (default "name").
	Field string
}

// MaxNameLength bounds a typed name ("NAME (unverified)" must stay within
// the 128 characters of requestedBy).
const MaxNameLength = 100

// Who implements Identity. r.ParseForm must have run.
func (t TypedName) Who(r *http.Request) (Person, error) {
	field := t.Field
	if field == "" {
		field = "name"
	}
	name := strings.TrimSpace(r.PostFormValue(field))
	if err := checkName(name); err != nil {
		return Person{}, err
	}
	return Person{Name: name, Verified: false}, nil
}

func checkName(name string) error {
	if name == "" {
		return errors.New("Type your name.")
	}
	if utf8.RuneCountInString(name) > MaxNameLength || !utf8.ValidString(name) {
		return errors.New("The name is too long.")
	}
	for _, r := range name {
		if unicode.IsControl(r) || r == ' ' || r == ' ' {
			return errors.New("The name contains characters that are not allowed.")
		}
	}
	if strings.HasSuffix(strings.ToLower(name), "(unverified)") {
		return errors.New("Type only your name.")
	}
	return nil
}
