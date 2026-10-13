// Command sbx-init is the tiny static PID 1 copied into sandbox images.
package main

import (
	"os"

	"github.com/rielicat/bothy/internal/sbxinit"
)

func main() { os.Exit(sbxinit.Main(os.Args[1:])) }
