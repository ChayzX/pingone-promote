package pingone

import (
	"errors"
	"net/url"
	"strconv"
	"strings"
)

// Singleton snapshots have service-defined identities rather than a final URL
// ID. Keep this list explicit: arbitrary type: values are not resource bindings.
var singletonSnapshotIDs = map[string]string{
	"brandingSettings":      "type:brandingsettings",
	"mfaSettings":           "type:mfasettings",
	"notificationsSettings": "type:notificationssettings",
	"notificationsSettings/emailDeliverySettings": "type:notificationssettings:emaildeliverysettings",
	"ssoCookieSettings":                           "type:ssocookiesettings",
}

func resourceIdentityPart(s string) bool {
	return len(s) <= 256 && pinnedID(s)
}

func snapshotFlowVersion(s string) bool {
	n, err := strconv.ParseUint(s, 10, 64)
	return err == nil && strconv.FormatUint(n, 10) == s
}

// ValidResourceID accepts ordinary resource IDs and the native snapshot
// identities supported below. Environment, snapshot and request-file identities
// retain their separate, stricter validation.
func ValidResourceID(id string) bool {
	if resourceIdentityPart(id) && ((id[0] >= 'a' && id[0] <= 'z') || (id[0] >= 'A' && id[0] <= 'Z') || (id[0] >= '0' && id[0] <= '9')) {
		return true
	}
	for _, known := range singletonSnapshotIDs {
		if id == known {
			return true
		}
	}
	if rest, ok := strings.CutPrefix(id, "flowId:"); ok {
		flow, version, found := strings.Cut(rest, "-version:")
		return found && resourceIdentityPart(flow) && snapshotFlowVersion(version)
	}
	return false
}

// snapshotResourceType binds a native resource identity to its exact URL shape.
// The caller separately enforces the configured API origin with apiURL.
func snapshotResourceType(u *url.URL, env, id string) (string, error) {
	bad := errors.New("inventory resource URL does not match its original identity")
	prefix := EnvPath(env) + "/"
	if !strings.HasPrefix(u.Path, prefix) || u.Fragment != "" || strings.Contains(u.Path, "//") || !ValidResourceID(id) {
		return "", bad
	}
	tail := strings.TrimSuffix(strings.TrimPrefix(u.Path, prefix), "/")
	parts := strings.Split(tail, "/")
	for _, part := range parts {
		if !resourceIdentityPart(part) {
			return "", bad
		}
	}
	// Native snapshots use these expansions to include scope references. They
	// change the read representation, not the resource identity. Accept exactly
	// one known expansion on its matching path; callers then drop that query.
	if u.RawQuery != "" && !(len(parts) == 4 && parts[0] == "applications" &&
		((parts[2] == "attributes" && u.RawQuery == "expand=oidcScopes") ||
			(parts[2] == "grants" && u.RawQuery == "expand=scopes"))) {
		return "", bad
	}
	if known, ok := singletonSnapshotIDs[tail]; ok {
		if id != known {
			return "", bad
		}
		return strings.Join(parts, " / "), nil
	}
	if len(parts) == 5 && parts[0] == "flows" && parts[2] == "versions" && parts[4] == "details" {
		if !snapshotFlowVersion(parts[3]) || id != "flowId:"+parts[1]+"-version:"+parts[3] {
			return "", bad
		}
		return "flows / versions / details", nil
	}
	if len(parts) < 2 || len(parts)%2 != 0 || parts[len(parts)-1] != id {
		return "", bad
	}
	kinds := make([]string, 0, len(parts)/2)
	for i := 0; i < len(parts); i += 2 {
		kinds = append(kinds, parts[i])
	}
	return strings.Join(kinds, " / "), nil
}
