package secretscan

import "testing"

// Masked values are whole strings; asterisks inside text are not.
func TestMaskedValueIsAWholeString(t *testing.T) {
	for _, line := range []string{`  secret = "******"`, `"value" : "********",`, `"{\"clientSecret\":\"******\"}"`} {
		if len(ScanText("f", []byte(line))) != 1 {
			t.Errorf("not found: %s", line)
		}
	}
	for _, line := range []string{
		`css = "\n  :root {\n    /*******\n    * COMPANY LOGO\n    *******/\n"`,
		`"value" : "const masked = '*******' + phone.slice(-3);"`,
		`"value" : "x = \"a******b\""`,
	} {
		if f := ScanText("f", []byte(line)); len(f) != 0 {
			t.Errorf("false finding %v: %s", f, line)
		}
	}
}
