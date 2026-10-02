package ble

import (
	"strings"
	"testing"
)

func TestValidatePairCommand(t *testing.T) {
	secret := strings.Repeat("a", 32) // generateSecret() output length

	if err := validatePairCommand("Office Desk", secret); err != nil {
		t.Errorf("normal name rejected: %v", err)
	}
	// "PAIR:" + name + ":" + secret must fit the light's 64-byte characteristic.
	if err := validatePairCommand(strings.Repeat("n", 26), secret); err != nil {
		t.Errorf("26-char name should fit exactly: %v", err)
	}
	if err := validatePairCommand(strings.Repeat("n", 27), secret); err == nil {
		t.Error("27-char name should be rejected (would truncate the secret)")
	}
	if err := validatePairCommand("Desk:2", secret); err == nil {
		t.Error("name with ':' should be rejected (breaks PAIR parsing)")
	}
}

func TestPairResult(t *testing.T) {
	if err := pairResult("PAIR_OK"); err != nil {
		t.Errorf("PAIR_OK: got %v, want nil", err)
	}
	for _, resp := range []string{"ERR:ALREADY_PAIRED", "ERR:BAD_FORMAT", "ERR:EMPTY_FIELDS"} {
		if err := pairResult(resp); err == nil || err == errPairPending {
			t.Errorf("%s: got %v, want a pairing error", resp, err)
		}
	}
	// Values seen before the light has processed the command: keep waiting.
	for _, resp := range []string{"PAIR:Office Desk:abc123", "STATUS:UNPAIRED", ""} {
		if err := pairResult(resp); err != errPairPending {
			t.Errorf("%q: got %v, want errPairPending", resp, err)
		}
	}
}
