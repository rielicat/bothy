//go:build !sbxtesthooks

package sandboxd

import "testing"

// Release builds never let the environment downgrade the engine API.
func TestTestAPILimitIgnoredWithoutTag(t *testing.T) {
	c, err := ParseConfig([]string{"BOTHY_SBX_IMAGES=base=" + testImage, "BOTHY_SBX_TEST_API_LIMIT=1.44"})
	if err != nil || c.apiLimit != "" {
		t.Fatalf("apiLimit = %q, %v", c.apiLimit, err)
	}
}
