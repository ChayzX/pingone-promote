package resourcecatalog

import "testing"

func TestPinnedInventoryIncludesOnlyConfiguration(t *testing.T) {
	if len(All()) != 101 {
		t.Fatalf("provider inventory changed: %d", len(All()))
	}
	for _, typ := range []string{"pingone_notification_template_content", "pingone_sign_on_policy_action", "pingone_verify_voice_phrase", "pingone_davinci_flow"} {
		if !Allowed(typ) {
			t.Fatalf("configuration type missing: %s", typ)
		}
	}
	for _, typ := range []string{"pingone_user", "pingone_user_role_assignment", "pingone_role_assignment_user", "pingone_user_group_assignment", "pingone_user_application_role_assignment", "pingone_unknown", "pingone_session"} {
		if Allowed(typ) {
			t.Fatalf("non-configuration type allowed: %s", typ)
		}
	}
}
