package store

import "testing"

func TestGlobMatch(t *testing.T) {
	tests := []struct {
		pattern, s string
		want       bool
	}{
		// Empty pattern and empty string.
		{"", "", true},
		{"", "a", false},
		{"a", "", false},

		// Plain characters must match exactly.
		{"hello", "hello", true},
		{"hello", "hell", false},
		{"hello", "helloo", false},

		// * matches any sequence, including the empty one.
		{"*", "", true},
		{"*", "anything", true},
		{"*", "a/b", true}, // unlike path.Match, * matches "/"
		{"**", "abc", true},
		{"h*llo", "hllo", true}, // * matches nothing
		{"h*llo", "hello", true},
		{"h*llo", "heeeeello", true},
		{"h*llo", "hellol", false},
		{"user:*", "user:42", true},
		{"user:*", "session:1", false},
		{"*:42", "user:42", true},
		{"a*b*c", "aXXbYYc", true},

		// Cases that need backtracking: the first guess for * is wrong.
		{"*ab", "aab", true},      // * must take the first "a", not nothing
		{"a*bc", "abcbc", true},   // the first "bc" is a false end
		{"*a*b", "xaxxaxb", true}, // two stars
		{"a*a", "a", false},       // * can't make one "a" count twice
		{"*abc", "ababab", false}, // never finds "abc"

		// ? matches exactly one character.
		{"?", "a", true},
		{"?", "", false},
		{"?", "ab", false},
		{"h?llo", "hello", true},
		{"h?llo", "hllo", false},
		{"ab?", "ab", false},
		{"??", "é", true}, // é is 2 bytes in UTF-8, and Redis matches bytes

		// [abc] sets.
		{"[abc]", "b", true},
		{"[abc]", "d", false},
		{"h[ae]llo", "hallo", true},
		{"h[ae]llo", "hillo", false},

		// [a-z] ranges.
		{"[a-c]", "a", true},
		{"[a-c]", "b", true},
		{"[a-c]", "c", true},
		{"[a-c]", "d", false},
		{"[c-a]", "b", true}, // a backwards range works too, like Redis
		{"[0-9][0-9]", "42", true},

		// [^abc] negation.
		{"[^abc]", "d", true},
		{"[^abc]", "a", false},
		{"[^a-c]x", "dx", true},
		{"[^a-c]x", "bx", false},

		// \ escapes a special character.
		{`\*`, "*", true},
		{`\*`, "x", false}, // escaped: \* is NOT a wildcard...
		{"*", "x", true},   // ...but a plain * is
		{`a\?b`, "a?b", true},
		{`a\?b`, "axb", false},
		{`\[a]`, "[a]", true},
		{`[\]]`, "]", true}, // escaping inside a set
		{`a\`, `a\`, true},  // a trailing \ has nothing to escape: literal \

		// An unclosed [ behaves like Redis: the end of the pattern closes the set.
		{"[", "[", false}, // a bare "[" is an empty set: never matches
		{"[", "a", false},
		{"[abc", "a", true},
		{"[abc", "[abc", false},
	}

	for _, tt := range tests {
		if got := globMatch(tt.pattern, tt.s); got != tt.want {
			t.Errorf("globMatch(%q, %q) = %v, want %v", tt.pattern, tt.s, got, tt.want)
		}
	}
}
