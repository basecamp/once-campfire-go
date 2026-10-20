package rails

import (
	"crypto/rand"
	"strings"
)

// StorageKey matches Active Storage's 28-character lowercase base-36 keys.
func StorageKey() string {
	const alphabet = "0123456789abcdefghijklmnopqrstuvwxyz"
	key := make([]byte, 0, 28)
	var random [64]byte
	for len(key) < 28 {
		if _, err := rand.Read(random[:]); err != nil {
			panic(err)
		}
		for _, b := range random {
			if b < 252 {
				key = append(key, alphabet[int(b)%36])
				if len(key) == 28 {
					break
				}
			}
		}
	}
	return string(key)
}

// Filename implements ActiveStorage::Filename#sanitized for display and indexing.
func Filename(name string) string {
	return strings.Map(func(c rune) rune {
		if strings.ContainsRune("\u202e%$|:;/<>?*\"\t\r\n\\", c) {
			return '-'
		}
		return c
	}, strings.Trim(name, "\x00\t\n\v\f\r "))
}
