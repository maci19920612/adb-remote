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

func ParseCommand(commands []*Command[BaseCommand]) {
	args := os.Args
	if len(args) < 2 {
		printGlobalHelp(commands)
		return
	}
	commandArg := args[1]
	var targetCommand *Command[BaseCommand] = nil
	for index := range commands {
		if commands[index].Name == commandArg {
			targetCommand = commands[index]
		}
	}
	if targetCommand == nil {
		printGlobalHelp(commands)
		return
	}
	parameter, err := targetCommand.ParameterFactory()
	if err != nil {
		panic(err)
	}
	targetFlagSet := parameter.GetFlagSet()
	err = targetFlagSet.Parse(args[2:])
	if err != nil {
		panic(err)
	}
	if parameter.IsHelp() {
		targetFlagSet.Usage()
		return
	}
	err = targetCommand.Handler(parameter)
	if err != nil {
		panic(err)
	}
}
