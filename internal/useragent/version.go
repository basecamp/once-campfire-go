package useragent

import (
	"regexp"
	"strings"
)

var comparableVersion = regexp.MustCompile(`^[0-9]+(?:\.|$)`)
var versionSequences = regexp.MustCompile(`[0-9]+|[A-Za-z][0-9A-Za-z-]*$`)

func VersionParts(s string) []string {
	if rubyStrip(s) == "" {
		return []string{}
	}
	if !comparableVersion.MatchString(s) {
		return []string{"s:" + s}
	}
	var result []string
	for _, part := range versionSequences.FindAllString(s, -1) {
		if part[0] >= '0' && part[0] <= '9' {
			part = strings.TrimLeft(part, "0")
			if part == "" {
				part = "0"
			}
			result = append(result, "i:"+part)
		} else {
			result = append(result, "s:"+part)
		}
	}
	return result
}
func Compare(a, b string) int {
	if !comparableVersion.MatchString(a) {
		if a == b {
			return 0
		}
		return -1
	}
	ours, theirs := VersionParts(a), VersionParts(b)
	for i := 0; i < 6; i++ {
		x, y := "i:0", "i:0"
		if i < len(ours) {
			x = ours[i]
		}
		if i < len(theirs) {
			y = theirs[i]
		}
		if x == y {
			continue
		}
		if x[0] != y[0] {
			if x[0] == 's' {
				return -1
			}
			return 1
		}
		if x[0] == 'i' && len(x) != len(y) {
			if len(x) < len(y) {
				return -1
			}
			return 1
		}
		return strings.Compare(x[2:], y[2:])
	}
	return 0
}
