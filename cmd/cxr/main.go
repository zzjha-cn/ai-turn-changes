// Command cxr is the Codex turn-level rewind tool.
package main

import (
	"os"

	"github.com/zzjha-cn/codex-rewind/internal/cli"
)

func main() {
	os.Exit(cli.Main(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}
