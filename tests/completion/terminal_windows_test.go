//go:build completion && windows

package completion_test

import "testing"

func testInteractive(t *testing.T, shell string, _ shellFixture) {
	t.Fatalf("%s terminal integration requires Linux or macOS; run PowerShell checks on Windows", shell)
}
