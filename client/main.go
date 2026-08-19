package main

import (
	"adb-remote.maci.team/client/command"
	"adb-remote.maci.team/client/di"
	"fmt"
	"os"
)

func main() {
	container := di.CreateContainer()
	var commands []*command.Command[command.BaseCommand]
	if err := container.Resolve(&commands); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := command.ParseCommand(commands); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
