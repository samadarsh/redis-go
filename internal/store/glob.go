package store

// globMatch reports whether s matches pattern, using the same glob rules
// as Redis's KEYS command (stringmatchlen in Redis's util.c):
//
//   - "*" matches any sequence of characters, including none
//   - "?" matches exactly one character
//   - "[abc]" matches one character from the set
//   - "[a-z]" matches one character in the range (a reversed range like [z-a] also works)
//   - "[^abc]" matches one character NOT in the set
//   - "\x" matches the character x literally, e.g. \* matches only "*"
//
// We don't use path.Match or filepath.Match because their * stops at "/",
// while in Redis * matches everything: KEYS * must return "a/b".
//
// Matching works on bytes, like Redis, so ? matches one byte. A character
// such as "é" is two bytes in UTF-8 and therefore needs ?? to match.
func globMatch(pattern, s string) bool {
	p, i := 0, 0 // our position in pattern and in s

	// Backtracking state for the most recent *. starP is the index of the
	// * in pattern (-1 means we haven't seen one yet), and starI is how much
	// of s that * has absorbed so far: s[that point:starI].
	starP, starI := -1, 0

	for i < len(s) {
		// 1. A *: first try letting it match nothing. Remember where it is
		//    so we can come back and make it match more if needed.
		if p < len(pattern) && pattern[p] == '*' {
			starP, starI = p, i
			p++
			continue
		}

		// 2. Any other token (a letter, ?, [set] or \x) matches exactly one
		//    byte of s. If it matches, move both positions forward.
		if p < len(pattern) {
			if next, ok := matchToken(pattern, p, s[i]); ok {
				p = next
				i++
				continue
			}
		}

		// 3. Mismatch (or the pattern ran out before s did). If there was an
		//    earlier *, backtrack: let that * absorb one more byte of s, and
		//    retry the rest of the pattern from just after the *.
		if starP >= 0 {
			starI++
			i = starI
			p = starP + 1
			continue
		}

		// 4. Mismatch and no * to fall back on: no match.
		return false
	}

	// s is fully consumed. Any trailing *s can match the empty string; any
	// other leftover pattern means s was too short (e.g. "ab?" vs "ab").
	for p < len(pattern) && pattern[p] == '*' {
		p++
	}
	return p == len(pattern)
}

// matchToken tries to match the single (non-*) token starting at pattern[p]
// against the byte c. It returns the index just after the token, and
// whether it matched.
func matchToken(pattern string, p int, c byte) (next int, ok bool) {
	switch pattern[p] {
	case '?':
		return p + 1, true

	case '[':
		return matchSet(pattern, p+1, c)

	case '\\':
		// \x matches x literally. A \ at the very end of the pattern has
		// nothing to escape, so like Redis we treat it as a literal \.
		if p+1 < len(pattern) {
			return p + 2, pattern[p+1] == c
		}
		return p + 1, c == '\\'

	default:
		return p + 1, pattern[p] == c
	}
}

// matchSet matches a [...] set against c. p is the index just after '['.
// It follows Redis's rules exactly, including for an unclosed set: "[abc"
// with no ']' acts as if the end of the pattern closed it, so it matches
// one of a, b or c. A bare "[" is an empty set and never matches.
func matchSet(pattern string, p int, c byte) (next int, ok bool) {
	negate := p < len(pattern) && pattern[p] == '^'
	if negate {
		p++
	}

	matched := false
	for {
		switch {
		case p >= len(pattern):
			// Unclosed set: the end of the pattern closes it.
			return p, matched != negate

		case pattern[p] == '\\' && p+1 < len(pattern):
			// \x inside a set: x is a literal member, e.g. [\]] matches "]".
			if pattern[p+1] == c {
				matched = true
			}
			p += 2

		case pattern[p] == ']':
			return p + 1, matched != negate // skip past the ']'

		case p+2 < len(pattern) && pattern[p+1] == '-':
			// A range like a-z. Redis also accepts it backwards (z-a).
			lo, hi := pattern[p], pattern[p+2]
			if lo > hi {
				lo, hi = hi, lo
			}
			if lo <= c && c <= hi {
				matched = true
			}
			p += 3

		default:
			// A plain member of the set.
			if pattern[p] == c {
				matched = true
			}
			p++
		}
	}
}
