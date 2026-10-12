//go:build !unix

package sbxinit

import (
	"fmt"
	"io"
)

func unsupported(w io.Writer) int {
	fmt.Fprintln(w, "sbx-init: only runs inside Linux tool boxes")
	return exitInternal
}

func initMain(stderr io.Writer) int                    { return unsupported(stderr) }
func runMain(_ []string, stderr io.Writer) int         { return unsupported(stderr) }
func killallMain(_, stderr io.Writer) int              { return unsupported(stderr) }
func countMain(_, stderr io.Writer) int                { return unsupported(stderr) }
func selftestMain(_ []string, _, stderr io.Writer) int { return unsupported(stderr) }
