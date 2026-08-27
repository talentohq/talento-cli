package releaseversion

import (
	"fmt"
	"regexp"
	"strings"
)

var semanticVersion = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?(?:\+([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?$`)

// Normalize returns the release version used in binaries, archive names,
// plugin manifests, and package-manager metadata. Release tags add exactly one
// leading v; every other release surface uses the normalized value.
func Normalize(value string) (string, error) {
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(value, "v")
	match := semanticVersion.FindStringSubmatch(value)
	if match == nil || invalidNumericPrerelease(match[4]) {
		return "", fmt.Errorf("%q is not a valid semantic version", value)
	}
	return value, nil
}

func Tag(value string) (string, error) {
	version, err := Normalize(value)
	if err != nil {
		return "", err
	}
	return "v" + version, nil
}

func invalidNumericPrerelease(value string) bool {
	for _, identifier := range strings.Split(value, ".") {
		if len(identifier) > 1 && identifier[0] == '0' && allDigits(identifier) {
			return true
		}
	}
	return false
}

func allDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, current := range value {
		if current < '0' || current > '9' {
			return false
		}
	}
	return true
}
