package ble

import "testing"

func TestSanitizeVersion(t *testing.T) {
	cases := map[string]string{
		"1.2.0":             "1.2.0",
		" 1.2.0\n":          "1.2.0",
		"dev":               "dev",
		"1.3.0-rc.1+build5": "1.3.0-rc.1+build5",
		"":                  "",
		"1.2<script>":       "", // anything outside version characters is rejected
		"1.2\x00\xff":       "", // garbled read
	}
	for in, want := range cases {
		if got := sanitizeVersion(in); got != want {
			t.Errorf("sanitizeVersion(%q) = %q, want %q", in, got, want)
		}
	}
}
