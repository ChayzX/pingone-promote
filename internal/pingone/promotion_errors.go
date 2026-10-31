package pingone

import (
	"encoding/json"
	"strings"
)

// SummarizePromotionErrors returns bounded, tenant-safe diagnostics for both
// the legacy errors field and PingOne's promotionErrors string array.
func SummarizePromotionErrors(legacy, promotionErrors json.RawMessage) []string {
	if !hasPromotionErrors(legacy) && !hasPromotionErrors(promotionErrors) {
		return nil
	}
	var entries []string
	for _, raw := range []json.RawMessage{legacy, promotionErrors} {
		if !hasPromotionErrors(raw) {
			continue
		}
		var value any
		if json.Unmarshal(raw, &value) != nil {
			entries = append(entries, genericPromotionError)
			continue
		}
		switch values := value.(type) {
		case []any:
			for _, value := range values {
				entries = append(entries, summarizePromotionError(value))
			}
		default:
			entries = append(entries, summarizePromotionError(value))
		}
	}
	seen := map[string]bool{}
	result := make([]string, 0, min(len(entries), 5))
	for _, entry := range entries {
		if seen[entry] {
			continue
		}
		seen[entry] = true
		result = append(result, entry)
		if len(result) == 5 {
			break
		}
	}
	return result
}

const genericPromotionError = "PingOne reported a promotion error; inspect status in PingOne."

func hasPromotionErrors(raw json.RawMessage) bool {
	if len(raw) == 0 || string(raw) == "null" {
		return false
	}
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return true
	}
	switch value := value.(type) {
	case nil:
		return false
	case []any:
		return len(value) > 0
	case map[string]any:
		return len(value) > 0
	case string:
		return value != ""
	default:
		return true
	}
}

func summarizePromotionError(value any) string {
	var raw string
	switch value := value.(type) {
	case string:
		raw = value
	case map[string]any:
		if code, ok := value["code"].(string); ok {
			return summarizePromotionCode(code)
		}
		return genericPromotionError
	default:
		return genericPromotionError
	}
	// PingOne can prefix an encoded error object with an HTTP status and quote
	// it. Decode only the object and use its code as a category, never message
	// text or identifiers.
	start := strings.Index(raw, "{")
	if start < 0 {
		return genericPromotionError
	}
	var detail struct {
		Code string `json:"code"`
	}
	if json.NewDecoder(strings.NewReader(raw[start:])).Decode(&detail) != nil {
		return genericPromotionError
	}
	return summarizePromotionCode(detail.Code)
}

func summarizePromotionCode(code string) string {
	if code == "UNEXPECTED_ERROR" {
		return "PingOne reported an unexpected promotion error; inspect status in PingOne."
	}
	return genericPromotionError
}
