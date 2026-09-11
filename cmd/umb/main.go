// Command umb is the Umbral command-line client. It talks JSON-RPC to umbrald over the
// local socket and autostarts the daemon when it is not running (REQ-CLI-003).
//
// T-F0-01 leaves it as a runnable skeleton; the client arrives in T-F0-11.
package main

import (
	"fmt"
	"os"
)

// exitUnavailable is the sysexits.h EX_UNAVAILABLE code that REQ-CLI-003 reserves for
// "the daemon could not be started within the timeout".
const exitUnavailable = 69

func main() {
	fmt.Fprintln(os.Stderr, "umb is not implemented yet (T-F0-11)")
	os.Exit(exitUnavailable)
}
