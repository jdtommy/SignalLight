package zoom

import (
	"testing"
)

func TestCheckMeetingActiveMultipleCalls(t *testing.T) {
	// Call CheckMeetingActive multiple times to verify no panic or callback accumulation
	for i := 0; i < 2500; i++ {
		_ = CheckMeetingActive()
	}
}
