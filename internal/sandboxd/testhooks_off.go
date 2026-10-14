//go:build !sbxtesthooks

package sandboxd

// testAPILimit is BOTHY_SBX_TEST_API_LIMIT in binaries built with the
// sbxtesthooks tag (see testhooks_on.go); release builds ignore it.
func testAPILimit(map[string]string) string { return "" }
