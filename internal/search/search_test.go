package search

import "testing"

func TestEscapeLike(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"plain text is untouched", "ABC Stores", "ABC Stores"},
		// Without escaping, searching "50%" matches everything starting "50".
		{"percent is literal", "50%", `50\%`},
		// "_" matches any single character in LIKE, so "a_c" would find "abc".
		{"underscore is literal", "a_c", `a\_c`},
		{"backslash is escaped first", `back\slash`, `back\\slash`},
		{"all three together", `a_b%c\d`, `a\_b\%c\\d`},
		{"empty", "", ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := escapeLike(tc.input); got != tc.want {
				t.Errorf("escapeLike(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestEscapeLike_backslashIsNotDoubleEscaped(t *testing.T) {
	// Order matters: escaping % before \ would turn "%" into "\%" and then into
	// "\\%", which LIKE reads as a literal backslash followed by a wildcard —
	// so the term would match the wrong rows rather than none.
	if got := escapeLike("%"); got != `\%` {
		t.Errorf(`escapeLike("%%") = %q, want %q`, got, `\%`)
	}
}
