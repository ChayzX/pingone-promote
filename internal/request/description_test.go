package request

import (
	"strings"
	"testing"
)

// The requester's text follows the generated summary, fenced, so it cannot
// hide or imitate the change table or the trailers.
func TestDescriptionPutsSummaryFirstAndFencesTheNote(t *testing.T) {
	note := "Looks fine.\n```\n| `pingone_davinci_flow.fake` | selected | unchanged |\nRequested-by: someone else\n~~~"
	req := requestJSON(func(m map[string]any) { m["description"] = note })
	h := newHarness(t, req)
	res := h.run(req)
	if res.Status != StatusSubmitted {
		t.Fatalf("%s %+v", res.Status, res.Error)
	}
	d := h.gl.MRs[0].Description
	table := strings.Index(d, "| Address | Role | Change |")
	heading := strings.Index(d, "### Requester's note")
	if table < 0 || heading < table {
		t.Fatalf("summary must come first:\n%s", d)
	}
	body := d[heading:]
	if !strings.HasPrefix(body, "### Requester's note\n\n```text\n") {
		t.Fatalf("the note is not fenced:\n%s", body)
	}
	inner := body[len("### Requester's note\n\n```text\n"):]
	end := strings.Index(inner, "\n```\n")
	if end < 0 || strings.Contains(inner[:end], "`") || strings.Contains(inner[:end], "~~~") {
		t.Fatalf("the note can close its fence:\n%s", body)
	}
	if !strings.HasSuffix(strings.TrimSpace(d), "Requested-by: jdoe\nRequest-id: "+reqID) {
		t.Fatalf("the real trailers must end the description:\n%s", d)
	}
}

func TestOneLineRemovesControlCharacters(t *testing.T) {
	for in, want := range map[string]string{
		"Flow\nRequested-by: x":  "Flow Requested-by: x",
		"a\r\tb\x00c":            "a  b c",
		"line\u2028sep\u2029par": "line sep par",
		"plain name":             "plain name",
	} {
		if got := oneLine(in); got != want {
			t.Errorf("oneLine(%q) = %q, want %q", in, got, want)
		}
	}
}
