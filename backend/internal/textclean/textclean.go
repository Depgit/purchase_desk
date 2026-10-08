// Package textclean tidies typed text and builds the comparison key used to block duplicate names.
package textclean

import (
	"regexp"
	"strings"
)

var nonAlnum = regexp.MustCompile(`[^a-z0-9]`)
var spaces = regexp.MustCompile(`\s+`)

// Key ignores case, spaces and punctuation, so "Soap Bar 20g" and "soap-bar 20 G" match.
func Key(s string) string { return nonAlnum.ReplaceAllString(strings.ToLower(s), "") }

// Clean trims and collapses spaces.
func Clean(s string) string { return strings.TrimSpace(spaces.ReplaceAllString(s, " ")) }

// Upper removes all spaces and upper-cases, for codes such as GSTIN, PAN and IFSC.
func Upper(s string) string { return strings.ToUpper(spaces.ReplaceAllString(s, "")) }

func Contains[T comparable](xs []T, v T) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}

func Uniq[T comparable](xs []T) []T {
	seen := map[T]bool{}
	out := []T{}
	for _, x := range xs {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}

func Default(s, def string) string {
	if strings.TrimSpace(s) == "" {
		return def
	}
	return s
}
