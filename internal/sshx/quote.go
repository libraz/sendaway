package sshx

import "strings"

// ShellQuote quotes s as a single POSIX shell word using single quotes.
func ShellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}

// EscapeExclude escapes the rsync pattern metacharacters in a literal path so that
// it matches only itself when used as an exclude entry.
func EscapeExclude(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := range len(s) {
		switch s[i] {
		case '\\', '*', '?', '[', ']':
			b.WriteByte('\\')
		}
		b.WriteByte(s[i])
	}
	return b.String()
}
