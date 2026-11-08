package main

import (
	"sort"
	"testing"
)

// Every text exists in both languages: a key in one map only would show its raw name on the other page.
func TestTextsInBothLanguages(t *testing.T) {
	for _, pair := range [][2]string{{"pt", "en"}, {"en", "pt"}} {
		var missing []string
		for k := range strs[pair[0]] {
			if _, ok := strs[pair[1]][k]; !ok {
				missing = append(missing, k)
			}
		}
		sort.Strings(missing)
		if len(missing) > 0 {
			t.Errorf("in %q but not in %q: %v", pair[0], pair[1], missing)
		}
	}
	if len(monthNames["pt"]) != len(monthNames["en"]) {
		t.Errorf("month names: %d pt, %d en", len(monthNames["pt"]), len(monthNames["en"]))
	}
}
