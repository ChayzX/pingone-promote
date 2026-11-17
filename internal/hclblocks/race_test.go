//go:build race

package hclblocks

// raceEnabled is true under the race detector, which slows code several
// times; wall-clock limits in tests are loosened under it.
const raceEnabled = true
