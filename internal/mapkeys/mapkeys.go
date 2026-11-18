// Package mapkeys provides deterministic iteration over string-keyed maps.
package mapkeys

import "sort"

// Sorted returns the keys in ascending order, including an empty slice for an empty map.
func Sorted[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
