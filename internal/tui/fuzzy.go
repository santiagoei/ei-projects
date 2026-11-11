package tui

import "strings"

// The matcher behind `/`. A dependency would bring fifty kilobytes of ranking
// nobody asked to change: what the cockpit needs is "do these characters
// appear in this order", which is a dozen lines and no new module.
//
// Matches never reorder anything. A session list is chronology and a commit
// list is history: a filter that sorted by score would hide the newest thing
// the user was looking for under a better-looking one.

// folds maps the accented Latin letters to their base, so typing "auditoria"
// finds "Auditoría y curaduría". A table and not a Unicode pipeline for the
// same reason colors.toml is read by hand: the vault and the session titles
// are Spanish, and making the user type the accent would be a worse tool.
var folds = strings.NewReplacer(
	"á", "a", "à", "a", "ä", "a", "â", "a", "ã", "a", "å", "a",
	"é", "e", "è", "e", "ë", "e", "ê", "e",
	"í", "i", "ì", "i", "ï", "i", "î", "i",
	"ó", "o", "ò", "o", "ö", "o", "ô", "o", "õ", "o",
	"ú", "u", "ù", "u", "ü", "u", "û", "u",
	"ý", "y", "ÿ", "y",
	"ñ", "n", "ç", "c",
	"ß", "ss", "ø", "o", "æ", "ae", "ð", "d", "þ", "th", "ł", "l",
)

// key is the shape a string is compared in: lowercase, folded.
func key(s string) string { return folds.Replace(strings.ToLower(s)) }

// fuzzyMatch is whether every character of the needle appears in the
// haystack, in order, with anything in between. Both sides are walked as
// runes: the titles are Spanish and the paths are not always ASCII, and a
// matcher that stepped through bytes would treat one character of "sesión" as
// two.
//
// An empty needle matches everything, which is what makes "no filter" and "a
// filter of nothing" the same code path.
func fuzzyMatch(haystack, needle string) bool {
	want := []rune(key(needle))
	if len(want) == 0 {
		return true
	}
	hay := []rune(key(haystack))
	pos := 0
	for _, wantRune := range want {
		for pos < len(hay) && hay[pos] != wantRune {
			pos++
		}
		if pos == len(hay) {
			return false
		}
		pos++
	}
	return true
}
