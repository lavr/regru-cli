package regruzone

import (
	"fmt"
	"strconv"
)

const maxTTL = 1<<31 - 1

func ParseTTL(s string) (int, error) {
	if s == "" {
		return 0, fmt.Errorf("invalid ttl %q: empty", s)
	}
	digits, mult := s, 1
	switch s[len(s)-1] {
	case 'm':
		digits, mult = s[:len(s)-1], 60
	case 'h':
		digits, mult = s[:len(s)-1], 3600
	case 'd':
		digits, mult = s[:len(s)-1], 86400
	case 'w':
		digits, mult = s[:len(s)-1], 604800
	}
	if digits == "" {
		return 0, fmt.Errorf("invalid ttl %q: no number", s)
	}
	for i := 0; i < len(digits); i++ {
		if digits[i] < '0' || digits[i] > '9' {
			return 0, fmt.Errorf("invalid ttl %q: unsupported format", s)
		}
	}
	n, err := strconv.Atoi(digits)
	if err != nil || n > maxTTL/mult {
		return 0, fmt.Errorf("invalid ttl %q: out of range", s)
	}
	return n * mult, nil
}
