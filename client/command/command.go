package command

import (
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"
)

var InvalidCommandArgumentType = errors.New("invalid command argument type")

// VerbosityDebug is the -verbosity flag value that enables debug logging
// and packet capture (see ParseCommand).
const VerbosityDebug = "debug"

// RegisterVerbosityFlag adds the -verbosity flag shared by every command:
// "default" (the normal log level) or "debug" (debug-level logging, plus a
// packet capture .pcap file — see transportLayer.Client.EnableDebugCapture).
func RegisterVerbosityFlag(flagSet *flag.FlagSet) *string {
	return flagSet.String("verbosity", "default", `Logging verbosity: "default" or "debug" (debug also writes a packet capture .pcap file next to the log)`)
}

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

	if parameter.Verbosity() == VerbosityDebug {
		if targetCommand.LogLevel != nil {
			targetCommand.LogLevel.Set(slog.LevelDebug)
		}
		if err := targetCommand.Client.EnableDebugCapture(targetCommand.PcapPath); err != nil {
			return fmt.Errorf("failed to enable debug packet capture: %w", err)
		}
	}

	if err := targetCommand.Client.Start(); err != nil {
		return fmt.Errorf("failed to connect to the transporter at %s: %w", targetCommand.Config.TransporterAddress, err)
	}
	defer targetCommand.Client.Close()

	return targetCommand.Handler(parameter)
}
