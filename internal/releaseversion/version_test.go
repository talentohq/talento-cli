package releaseversion

import "testing"

func TestNormalize(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		"1.2.3":               "1.2.3",
		"v1.2.3":              "1.2.3",
		" v1.2.3-rc.1+build ": "1.2.3-rc.1+build",
	}
	for input, expected := range tests {
		input, expected := input, expected
		t.Run(input, func(t *testing.T) {
			t.Parallel()
			actual, err := Normalize(input)
			if err != nil {
				t.Fatal(err)
			}
			if actual != expected {
				t.Fatalf("Normalize(%q) = %q, want %q", input, actual, expected)
			}
		})
	}
}

func TestNormalizeRejectsInvalidVersions(t *testing.T) {
	t.Parallel()
	for _, input := range []string{"", "vv1.2.3", "1.2", "01.2.3", "1.2.3-01", "1.2.3/asset"} {
		input := input
		t.Run(input, func(t *testing.T) {
			t.Parallel()
			if _, err := Normalize(input); err == nil {
				t.Fatalf("Normalize(%q) unexpectedly succeeded", input)
			}
		})
	}
}
