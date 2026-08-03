// Command draiver is a coordination substrate for supervising AI coding agents
// at the ticket level: an append-only, hash-chained per-ticket log plus a
// read-only board. Agents are cattle, tickets are pets.
package main

import (
	"os"

	"github.com/Dawil/draiver/cmd"
)

func main() {
	os.Exit(cmd.Execute())
}
