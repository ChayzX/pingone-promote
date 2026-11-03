package tfvars

import (
	"reflect"
	"testing"
)

func TestDiff(t *testing.T) {
	oldDoc := fullDocument()
	newDoc := fullDocument()

	// Cosmetic changes are not changes.
	f := newDoc.Flows["login"]
	f.Settings.CSSLinks = []string{"https://cdn.example.com/a.css", "https://cdn.example.com/b.css", "https://cdn.example.com/a.css"}
	newDoc.Flows["login"] = f
	setNodeProps(newDoc, "login", "j0eval", "")
	n := newDoc.Flows["login"].Graph.Nodes["j0eval"]
	n.Properties = nil
	newDoc.Flows["login"].Graph.Nodes["j0eval"] = n
	c := newDoc.Connections["pingone_sso"]
	c.Properties = ptr(`{"envId":{"value":"@@env:id@@","type":"string"},"clientSecret":{"type":"string","value":"@@secret:PingOne_DaVinci_connection_PingOne_clientSecret@@"},"clientId":{"type":"string","value":"fake-client-id"},"baseUrl":{"type":"string","value":"@@param:idp_base_url@@/as"}}`)
	newDoc.Connections["pingone_sso"] = c
	if got := Diff(oldDoc, newDoc); len(got) != 0 {
		t.Fatalf("cosmetic edits reported as changes: %+v", got)
	}

	// Real changes.
	f.Name = "Login v2"
	newDoc.Flows["login"] = f
	setNodeProps(newDoc, "login", "a1b2c3", `{"hmacKey": {"value": "@@secret:login_hmac_key@@"}, "username": {"value": "x"}, "support": {"value": "mailto:@@param:support_email@@"}}`)
	delete(newDoc.Flows, "password_reset")
	newDoc.Flows["new_flow"] = Flow{Name: "New", Tier: 0, Graph: Graph{Nodes: map[string]Node{}, Edges: map[string]Edge{}}}
	newDoc.Parameters["idp_base_url"] = "https://idp.test.example.com"
	newDoc.ImportIDs.Flows["login"] = "fake-flow-0099"

	want := []Change{
		{Collection: "parameters", Key: "idp_base_url", Kind: Modified, Fields: []string{"."}},
		{Collection: "davinci_flows", Key: "login", Kind: Modified, Fields: []string{"name", `graph.nodes["a1b2c3"].properties/username/value`}},
		{Collection: "davinci_flows", Key: "new_flow", Kind: Added},
		{Collection: "davinci_flows", Key: "password_reset", Kind: Removed},
		{Collection: "import_ids.davinci_flows", Key: "login", Kind: Modified, Fields: []string{"."}},
	}
	if got := Diff(oldDoc, newDoc); !reflect.DeepEqual(got, want) {
		t.Fatalf("diff:\n got %+v\nwant %+v", got, want)
	}
}
