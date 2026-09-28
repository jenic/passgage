// Command passgage is a portable age-backed password manager.
package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/jenic/passgage/internal/cli"
)

func main() {
	if err := cli.Execute(os.Args[1:]); err != nil {
		if !errors.Is(err, cli.ErrNoMatch) {
			fmt.Fprintln(os.Stderr, "Error:", err)
		}
		os.Exit(1)
	}
}
