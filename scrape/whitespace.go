package scrape

import "unicode"

// isKotlinWhitespace is Kotlin's Char.isWhitespace: Java's Character.isWhitespace plus the space separators
// (no-break spaces included).
func isKotlinWhitespace(r rune) bool {
	switch {
	case r >= 0x09 && r <= 0x0D, r >= 0x1C && r <= 0x1F:
		return true
	}
	return unicode.Is(unicode.Zs, r) || r == 0x2028 || r == 0x2029
}
