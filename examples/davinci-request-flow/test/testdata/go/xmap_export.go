// Scratch copy only (run_tests.sh copies it into a temporary clone): exposes
// the local app's mapping suggestions to the cross-check.
package app

import "pingone-promote/internal/pingone"

func SuggestMappings(a, b pingone.Inventory, native map[string]string) map[string]string {
	return suggestMappings(a, b, native)
}
