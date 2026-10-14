//go:build sbxtesthooks

package sandboxd

// testAPILimit caps the engine API version sandboxd negotiates, so the
// acceptance suite can run the Docker 25 bind-mount mode on a newer engine
// (deploy/sandbox/test/acceptance.sh, API_LIMIT=1.44). Only binaries built
// with -tags sbxtesthooks read it.
func testAPILimit(env map[string]string) string { return env["BOTHY_SBX_TEST_API_LIMIT"] }
