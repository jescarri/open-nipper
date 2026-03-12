// Command nipper is the Open-Nipper gateway and admin CLI.
package main

import (
	"github.com/jescarri/open-nipper/cli"
	_ "time/tzdata" // Embed IANA timezone database for time.LoadLocation
)

func main() {
	cli.Execute()
}
