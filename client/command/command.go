package command

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

var InvalidCommandArgumentType = errors.New("invalid command argument type")

func printGlobalHelp(commands []*Command[BaseCommand]) {
	fmt.Println("Program usage [command] [...args]")
	fmt.Println("Commands: ")
	leadingPadding := strings.Repeat(" ", 2)
	for _, command := range commands {
		fmt.Printf("%s%s\n", leadingPadding, command.Name)
	}
	fmt.Printf("\n To get additional help use the specific command --help flag\n")
}

func findCommand(commands []*Command[BaseCommand], name string) *Command[BaseCommand] {
	for _, command := range commands {
		if command.Name == name {
			return command
		}
	}
	return nil
}

// ParseCommand parses os.Args against the given commands, connects the
// shared transport client, and runs the matched command's handler.
func ParseCommand(commands []*Command[BaseCommand]) error {
	args := os.Args
	if len(args) < 2 {
		printGlobalHelp(commands)
		return nil
	}

	targetCommand := findCommand(commands, args[1])
	if targetCommand == nil {
		printGlobalHelp(commands)
		return nil
	}

	parameter, err := targetCommand.ParameterFactory()
	if err != nil {
		return err
	}
	targetFlagSet := parameter.GetFlagSet()
	if err := targetFlagSet.Parse(args[2:]); err != nil {
		return err
	}
	if parameter.IsHelp() {
		targetFlagSet.Usage()
		return nil
	}

	if err := targetCommand.Client.Start(); err != nil {
		return fmt.Errorf("failed to connect to the transporter at %s: %w", targetCommand.Config.TransporterAddress, err)
	}
	defer targetCommand.Client.Close()

	return targetCommand.Handler(parameter)
}
