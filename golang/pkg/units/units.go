// Package units parses human readable sizes.
package units

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

const sizeSyntax = "digits with an optional K, M, G or T suffix"

// ParseSize converts a decimal size with an optional K, M, G or T suffix to bytes.
// Suffixes are case insensitive and multiply the number by powers of 1024.
func ParseSize(s string) (int64, error) {
	digits, shift := s, 0
	if n := len(s); n > 0 {
		switch s[n-1] {
		case 'k', 'K':
			shift = 10
		case 'm', 'M':
			shift = 20
		case 'g', 'G':
			shift = 30
		case 't', 'T':
			shift = 40
		}
		if shift > 0 {
			digits = s[:n-1]
		}
	}
	if digits == "" || strings.Trim(digits, "0123456789") != "" {
		return 0, fmt.Errorf("invalid size %q: want %s", s, sizeSyntax)
	}
	v, err := strconv.ParseInt(digits, 10, 64)
	if err != nil || v > math.MaxInt64>>shift {
		return 0, fmt.Errorf("invalid size %q: exceeds %d bytes", s, int64(math.MaxInt64))
	}
	return v << shift, nil
}
