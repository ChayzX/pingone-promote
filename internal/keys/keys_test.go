package keys

import (
	"errors"
	"math/rand"
	"reflect"
	"regexp"
	"testing"
	"testing/quick"
)

var keyRE = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

func TestEncodeTable(t *testing.T) {
	for _, tc := range []struct{ name, key string }{
		{"Http", "Http"},
		{"pingone_sso", "pingone_sso"},
		{"PingOne SSO", "PingOne-20SSO"},
		{"Login - MFA", "Login-20---20MFA"},
		{"a-b", "a--b"},
		{"50%", "50-25"},
		{"café", "caf-C3-A9"},
		{"a/b.c", "a-2Fb-2Ec"},
		{"\x00\xff", "-00-FF"},
		{"--", "----"},
	} {
		got, err := Encode(tc.name)
		if err != nil || got != tc.key {
			t.Errorf("Encode(%q) = %q, %v; want %q", tc.name, got, err, tc.key)
		}
		back, err := Decode(tc.key)
		if err != nil || back != tc.name {
			t.Errorf("Decode(%q) = %q, %v; want %q", tc.key, back, err, tc.name)
		}
	}
}

func TestDecodeRejectsNonCanonical(t *testing.T) {
	for _, k := range []string{"", "-", "a-", "a-2", "-2D", "-41", "-5F", "-2d", "-zz", "a b", "a.b", string(make([]byte, 129))} {
		if _, err := Decode(k); err == nil {
			t.Errorf("Decode(%q) accepted a non-canonical key", k)
		}
	}
}

func TestEncodeLimits(t *testing.T) {
	if _, err := Encode(""); !errors.Is(err, ErrEmpty) {
		t.Fatalf("empty name: %v", err)
	}
	long := make([]byte, 43)
	for i := range long {
		long[i] = ' '
	}
	if _, err := Encode(string(long)); !errors.Is(err, ErrTooLong) {
		t.Fatalf("43 spaces encode to 129 chars: %v", err)
	}
	if k, err := Encode(string(long[:42])); err != nil || len(k) != 126 {
		t.Fatalf("42 spaces: %q %v", k, err)
	}
}

// The collision cases from the design review: the same variable name in
// different contexts, and the same flow-scoped name on different flows, must
// get distinct, stable keys.
func TestVariableKeyCollisions(t *testing.T) {
	cases := []struct{ name, context, flow, key string }{
		{"attempts", "company", "", "attempts_company"},
		{"attempts", "flow", "login", "attempts_flow_login"},
		{"attempts", "flow", "registration", "attempts_flow_registration"},
		{"attempts", "flowInstance", "", "attempts_flowInstance"},
		{"attempts", "user", "", "attempts_user"},
		// A name that looks like another variable's key suffix.
		{"attempts_flow", "company", "", "attempts-5Fflow_company"},
		{"a", "flow", "company", "a_flow_company"},
		{"a_flow", "company", "", "a-5Fflow_company"},
		// Flow keys may contain underscores; they are always last.
		{"x", "flow", "mfa_sub_flow", "x_flow_mfa_sub_flow"},
		{"api token", "company", "", "api-20token_company"},
	}
	seen := map[string]int{}
	for i, tc := range cases {
		got, err := VariableKey(tc.name, tc.context, tc.flow)
		if err != nil || got != tc.key {
			t.Fatalf("VariableKey(%q,%q,%q) = %q, %v; want %q", tc.name, tc.context, tc.flow, got, err, tc.key)
		}
		if !keyRE.MatchString(got) {
			t.Fatalf("key %q does not match the logical key pattern", got)
		}
		if j, dup := seen[got]; dup {
			t.Fatalf("cases %d and %d collide on %q", i, j, got)
		}
		seen[got] = i
		n, c, f, err := ParseVariableKey(got)
		if err != nil || n != tc.name || c != tc.context || f != tc.flow {
			t.Fatalf("ParseVariableKey(%q) = %q %q %q %v", got, n, c, f, err)
		}
		again, _ := VariableKey(tc.name, tc.context, tc.flow)
		if again != got {
			t.Fatal("variable key is not stable")
		}
	}
}

func TestVariableKeyRejects(t *testing.T) {
	for _, tc := range []struct{ name, context, flow string }{
		{"", "company", ""},
		{"x", "global", ""},
		{"x", "flow", ""},
		{"x", "flow", "bad key"},
		{"x", "company", "login"},
	} {
		if _, err := VariableKey(tc.name, tc.context, tc.flow); err == nil {
			t.Errorf("VariableKey(%q,%q,%q) accepted", tc.name, tc.context, tc.flow)
		}
	}
	for _, k := range []string{"x", "x_", "_company", "x_flow", "x_company_login", "x_global", "a_b_company", "x_flow_bad-"} {
		if _, _, _, err := ParseVariableKey(k); err == nil {
			t.Errorf("ParseVariableKey(%q) accepted", k)
		}
	}
}

func TestAssignIsOrderIndependent(t *testing.T) {
	names := []string{"Login", "PingOne SSO", "MFA subflow", "Http", "a-b", "a_b", "a b"}
	want, err := Assign(names)
	if err != nil {
		t.Fatal(err)
	}
	r := rand.New(rand.NewSource(1))
	for i := 0; i < 50; i++ {
		shuffled := append([]string(nil), names...)
		r.Shuffle(len(shuffled), func(a, b int) { shuffled[a], shuffled[b] = shuffled[b], shuffled[a] })
		got, err := Assign(shuffled)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("order changed the mapping: %v %v", got, err)
		}
	}
	var dup *DuplicateNameError
	if _, err := Assign([]string{"Login", "x", "Login"}); !errors.As(err, &dup) || !reflect.DeepEqual(dup.Names, []string{"Login"}) {
		t.Fatalf("duplicate names: %v", err)
	}
}

// Property: every short byte string round-trips, yields a valid key, and
// distinct names yield distinct keys.
func TestRoundTripProperty(t *testing.T) {
	cfg := &quick.Config{MaxCount: 5000, Rand: rand.New(rand.NewSource(7))}
	roundTrip := func(raw []byte) bool {
		if len(raw) == 0 || len(raw) > 40 {
			return true
		}
		name := string(raw)
		k, err := Encode(name)
		if err != nil || !keyRE.MatchString(k) {
			return false
		}
		back, err := Decode(k)
		return err == nil && back == name
	}
	if err := quick.Check(roundTrip, cfg); err != nil {
		t.Fatal(err)
	}
	injective := func(a, b []byte) bool {
		if len(a) == 0 || len(b) == 0 || len(a) > 40 || len(b) > 40 || string(a) == string(b) {
			return true
		}
		ka, _ := Encode(string(a))
		kb, _ := Encode(string(b))
		return ka != kb
	}
	if err := quick.Check(injective, cfg); err != nil {
		t.Fatal(err)
	}
	variable := func(raw []byte, ctx uint8, flow []byte) bool {
		if len(raw) == 0 || len(raw) > 30 || len(flow) == 0 || len(flow) > 30 {
			return true
		}
		c := VariableContexts[int(ctx)%len(VariableContexts)]
		fk := ""
		if c == "flow" {
			fk, _ = Encode(string(flow))
		}
		if c == "flow" && fk == "" {
			return true // flow name too long to be a key
		}
		k, err := VariableKey(string(raw), c, fk)
		if errors.Is(err, ErrTooLong) {
			return true
		}
		if err != nil || !keyRE.MatchString(k) {
			return false
		}
		n, gc, gf, err := ParseVariableKey(k)
		return err == nil && n == string(raw) && gc == c && gf == fk
	}
	if err := quick.Check(variable, cfg); err != nil {
		t.Fatal(err)
	}
}

func FuzzDecode(f *testing.F) {
	for _, s := range []string{"Http", "PingOne-20SSO", "a--b", "-C3-A9", "-41", "x_flow_login"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, key string) {
		if name, err := Decode(key); err == nil {
			again, err := Encode(name)
			if err != nil || again != key {
				t.Fatalf("decode accepted non-canonical key %q", key)
			}
		}
		if n, c, fk, err := ParseVariableKey(key); err == nil {
			again, err := VariableKey(n, c, fk)
			if err != nil || again != key {
				t.Fatalf("variable key %q is not canonical", key)
			}
		}
	})
}

func FuzzEncode(f *testing.F) {
	f.Add("Login - MFA")
	f.Fuzz(func(t *testing.T, name string) {
		k, err := Encode(name)
		if err != nil {
			return
		}
		if !keyRE.MatchString(k) {
			t.Fatalf("invalid key %q", k)
		}
		if back, err := Decode(k); err != nil || back != name {
			t.Fatalf("round trip failed for %q", name)
		}
	})
}
