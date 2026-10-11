package update

import (
	"cmp"
	"regexp"
	"strconv"
	"strings"
)

var (
	releaseVersion = regexp.MustCompile(`^v?(\d+)\.(\d+)\.(\d+)(?:-([0-9A-Za-z.-]+))?$`)
	gitDescribe    = regexp.MustCompile(`-\d+-g[0-9a-f]+$`) // 0.1.2-6-gd609b51
)

// IsRelease reports whether v is a release's version, X.Y.Z or X.Y.Z-pre,
// rather than a development build's: "dev", Xcode's "0.1", or git describe's
// "0.1.2-6-gd609b51".
func IsRelease(v string) bool {
	return releaseVersion.MatchString(v) && !gitDescribe.MatchString(v)
}

// Compare orders two release versions as semver does: by number, and a
// prerelease before its release.
func Compare(a, b string) int {
	ma, mb := releaseVersion.FindStringSubmatch(a), releaseVersion.FindStringSubmatch(b)
	if ma == nil || mb == nil {
		return cmp.Compare(a, b)
	}
	for i := 1; i <= 3; i++ {
		if c := cmp.Compare(number(ma[i]), number(mb[i])); c != 0 {
			return c
		}
	}
	switch pa, pb := ma[4], mb[4]; {
	case pa == pb:
		return 0
	case pa == "":
		return 1
	case pb == "":
		return -1
	default:
		return comparePrerelease(pa, pb)
	}
}

// comparePrerelease compares dot-separated identifiers, numbers as numbers.
func comparePrerelease(a, b string) int {
	ia, ib := strings.Split(a, "."), strings.Split(b, ".")
	for i := range min(len(ia), len(ib)) {
		na, errA := strconv.Atoi(ia[i])
		nb, errB := strconv.Atoi(ib[i])
		var c int
		switch {
		case errA == nil && errB == nil:
			c = cmp.Compare(na, nb)
		case errA == nil:
			c = -1
		case errB == nil:
			c = 1
		default:
			c = cmp.Compare(ia[i], ib[i])
		}
		if c != 0 {
			return c
		}
	}
	return cmp.Compare(len(ia), len(ib))
}

func number(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}
