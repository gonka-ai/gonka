package devshardobs

import (
	"sort"
	"strconv"
	"strings"
)

// SortVersions orders version names from oldest to newest using
// CompareVersionNames (numeric / dotted-numeric aware, not lexicographic).
func SortVersions(versions []string) []string {
	out := append([]string(nil), versions...)
	sort.SliceStable(out, func(i, j int) bool {
		return CompareVersionNames(out[i], out[j]) < 0
	})
	return out
}

// PrimaryVersion picks the newest version name for process-level obs pinning.
func PrimaryVersion(versions []string) string {
	if len(versions) == 0 {
		return ""
	}
	best := versions[0]
	for _, v := range versions[1:] {
		if CompareVersionNames(v, best) > 0 {
			best = v
		}
	}
	return best
}

// CompareVersionNames returns <0 if a is older than b, >0 if newer, 0 if equal.
//
// Names with a leading dotted-numeric prefix (optional leading "v"), such as
// "v2", "v10", "v0.2.11", compare by those numeric segments. Non-numeric names
// (e.g. "dev") sort below any numeric name; among themselves they use
// strings.Compare.
func CompareVersionNames(a, b string) int {
	an, aok := versionNumericParts(a)
	bn, bok := versionNumericParts(b)
	switch {
	case aok && bok:
		if c := compareIntSlices(an, bn); c != 0 {
			return c
		}
		return strings.Compare(a, b)
	case aok && !bok:
		return 1
	case !aok && bok:
		return -1
	default:
		return strings.Compare(a, b)
	}
}

func versionNumericParts(name string) ([]int, bool) {
	s := strings.TrimSpace(name)
	if s == "" {
		return nil, false
	}
	if s[0] == 'v' || s[0] == 'V' {
		s = s[1:]
	}
	if s == "" || s[0] < '0' || s[0] > '9' {
		return nil, false
	}

	var parts []int
	i := 0
	for i < len(s) {
		j := i
		for j < len(s) && s[j] >= '0' && s[j] <= '9' {
			j++
		}
		if j == i {
			break
		}
		n, err := strconv.Atoi(s[i:j])
		if err != nil {
			return nil, false
		}
		parts = append(parts, n)
		i = j
		if i < len(s) && s[i] == '.' {
			i++
			if i >= len(s) || s[i] < '0' || s[i] > '9' {
				return nil, false
			}
			continue
		}
		break
	}
	if len(parts) == 0 {
		return nil, false
	}
	return parts, true
}

func compareIntSlices(a, b []int) int {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		if a[i] != b[i] {
			if a[i] < b[i] {
				return -1
			}
			return 1
		}
	}
	switch {
	case len(a) < len(b):
		return -1
	case len(a) > len(b):
		return 1
	default:
		return 0
	}
}
