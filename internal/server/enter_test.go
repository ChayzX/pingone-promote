package server

import (
	"net/url"
	"regexp"
	"strings"
	"testing"
)

var (
	mainFormRE    = regexp.MustCompile(`(?s)<form [^>]*id="main"[^>]*>(.*?)</form>`)
	submitRE      = regexp.MustCompile(`<button[^>]*type="submit"[^>]*>`)
	searchInputRE = regexp.MustCompile(`<input type="search"[^>]*form="([^"]+)"`)
)

// firstMainButton is the button a browser presses on Enter in a field of
// the main form: the first submit button whose form is "main", in
// document order (the form's own buttons come first, since the form
// element precedes the fields).
func firstMainButton(t *testing.T, body string) string {
	t.Helper()
	if m := mainFormRE.FindStringSubmatch(body); m != nil {
		if b := submitRE.FindString(m[1]); b != "" {
			return b
		}
	}
	for _, b := range submitRE.FindAllString(body, -1) {
		if strings.Contains(b, `form="main"`) {
			return b
		}
	}
	t.Fatal("no submit button for the main form")
	return ""
}

// Enter in any field submits the step's main action, never Back, and Enter
// in the search box searches.
func TestEnterNeverGoesBack(t *testing.T) {
	e := newCoreEnv(t)
	b := e.browser()
	draft := b.newDraft()
	pages := map[string]string{"1": b.get(draft + "/scope").body}
	b.post(draft+"/save", mainForm("Core Tester", nil))
	pages["2"] = b.wait(b.post(draft+"/search", url.Values{"q": {"Cust"}})).body
	pages["3"] = b.get(draft + "/send").body
	want := map[string]string{"1": `value="next"`, "2": `value="next"`, "3": `value="preview"`}
	for step, body := range pages {
		if got := firstMainButton(t, body); !strings.Contains(got, want[step]) {
			t.Errorf("step %s: Enter presses %s", step, got)
		}
	}
	m := searchInputRE.FindStringSubmatch(pages["2"])
	if m == nil || m[1] == "main" {
		t.Fatalf("the search box must have a form of its own: %v", m)
	}
	if !strings.Contains(pages["2"], `id="`+m[1]+`"`) || !strings.Contains(pages["2"], `action="`+draft+`/search" id="`+m[1]+`"`) {
		t.Fatalf("the search form does not post to /search")
	}
}
