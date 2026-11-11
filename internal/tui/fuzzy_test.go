package tui

import "testing"

func TestFuzzyMatch(t *testing.T) {
	for _, tc := range []struct {
		haystack, needle string
		want             bool
	}{
		{"Auditoría y curaduría de ia_logs", "auditoria", true}, // accents folded
		{"Auditoría y curaduría de ia_logs", "AUDITORÍA", true}, // and case
		{"Auditoría y curaduría de ia_logs", "curad", true},
		{"Auditoría y curaduría de ia_logs", "iao", true},           // in order, far apart
		{"Auditoría y curaduría de ia_logs", "logs auditor", false}, // out of order
		{"Auditoría y curaduría de ia_logs", "zzz", false},
		{"50 tr/tr.fei/tr.fei.upgrade_a_4.5_OK.md", "trfeiup", true},
		{"50 tr/tr.fei/tr.fei.upgrade_a_4.5_OK.md", "upgrade.md", true},
		{"moodle405", "", true}, // an empty needle filters nothing
		{"moodle405", "406", false},
		{"ei-projects", "eip", true},
		{"9f8aef7 fix: themed UI", "themui", true},
		// multi-byte on both sides: the needle is walked in runes, so a
		// match may not split the character it lands on
		{"nota reciente 🎨", "🎨", true},
		{"nota reciente 🎨", "ntrec", true},
		{"日本語のセッション", "日本語", true},
		{"日本語のセッション", "語日", false},
	} {
		if got := fuzzyMatch(tc.haystack, tc.needle); got != tc.want {
			t.Errorf("fuzzyMatch(%q, %q) = %v, want %v", tc.haystack, tc.needle, got, tc.want)
		}
	}
}

// The fold is only ever a widening: plain lowercase ASCII passes through
// untouched, folding twice changes nothing more, and a needle the user copied
// out of a title keeps matching after both sides are folded.
func TestKeyOnlyFolds(t *testing.T) {
	for _, s := range []string{"auditoria", "tr.fei", "+ inbox.md", "ia_logs", "50 tr"} {
		if key(s) != s {
			t.Fatalf("key changed a plain string: %q -> %q", s, key(s))
		}
	}
	for _, s := range []string{"Auditoría", "EL NIÑO", "ZÜRICH", "Straße", "Ángel's 2024"} {
		if key(key(s)) != key(s) {
			t.Fatalf("folding twice changed more: %q -> %q", s, key(s))
		}
		if !fuzzyMatch(s, key(s)) {
			t.Fatalf("%q stopped matching its own folded form", s)
		}
	}
}
