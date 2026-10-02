package instance

import (
	"fmt"
	"testing"
	"time"
)

func TestAcquireAllowsOnlyOneHolder(t *testing.T) {
	// Unique name so the test never collides with a real running SignalLight.
	name := fmt.Sprintf(`Local\SignalLightTest-%d`, time.Now().UnixNano())

	first, err := Acquire(name)
	if err != nil || !first {
		t.Fatalf("first Acquire = %v, %v; want true, nil", first, err)
	}

	second, err := Acquire(name)
	if err != nil {
		t.Fatalf("second Acquire returned error: %v", err)
	}
	if second {
		t.Errorf("second Acquire = true; want false while the first holder is alive")
	}
}
