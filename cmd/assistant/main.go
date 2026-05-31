// SPDX-License-Identifier: GPL-3.0-only

package main

import (
	"os"

	"github.com/gratefulagents/assistant/internal/assistant"
)

func main() {
	os.Exit(assistant.Run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}
