package store

import (
	"crypto/rand"
	"fmt"
	"math/big"
	"strings"
)

// Generate samples uniformly from a passage-style ASCII character set.
func Generate(length int, pattern string) ([]byte, error) {
	if length < 1 || length > 1048576 {
		return nil, fmt.Errorf("password length must be 1..1048576")
	}
	classes := map[string]string{"[:lower:]": "abcdefghijklmnopqrstuvwxyz", "[:upper:]": "ABCDEFGHIJKLMNOPQRSTUVWXYZ", "[:digit:]": "0123456789", "[:alpha:]": "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ", "[:alnum:]": "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789", "[:punct:]": "!\"#$%&'()*+,-./:;<=>?@[\\]^_`{|}~"}
	var chars []byte
	seen := map[byte]bool{}
	add := func(c byte) {
		if !seen[c] {
			chars = append(chars, c)
			seen[c] = true
		}
	}
	for i := 0; i < len(pattern); {
		matched := false
		for k, v := range classes {
			if strings.HasPrefix(pattern[i:], k) {
				for j := range v {
					add(v[j])
				}
				i += len(k)
				matched = true
				break
			}
		}
		if matched {
			continue
		}
		if strings.HasPrefix(pattern[i:], "[:") {
			return nil, fmt.Errorf("unsupported character class")
		}
		if pattern[i] != '\\' && i+2 < len(pattern) && pattern[i+1] == '-' {
			a, b := pattern[i], pattern[i+2]
			if b < a || b > 126 {
				return nil, fmt.Errorf("invalid character range")
			}
			for n := int(a); n <= int(b); n++ {
				add(byte(n))
			}
			i += 3
			continue
		}
		if pattern[i] == '\\' {
			i++
			if i == len(pattern) {
				return nil, fmt.Errorf("unfinished character escape")
			}
		}
		if pattern[i] < 33 || pattern[i] > 126 {
			return nil, fmt.Errorf("character sets must use printable ASCII")
		}
		add(pattern[i])
		i++
	}
	if len(chars) == 0 {
		return nil, fmt.Errorf("empty character set")
	}
	out := make([]byte, length)
	for i := range out {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(chars))))
		if err != nil {
			return nil, err
		}
		out[i] = chars[n.Int64()]
	}
	return out, nil
}
