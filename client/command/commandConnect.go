package command

import (
	"adb-remote.maci.team/client/config"
	"adb-remote.maci.team/client/transportLayer"
	"flag"
	"fmt"
	"log/slog"
)

func CreateConnectCommand(logger *slog.Logger, client *transportLayer.Client, config *config.ClientConfiguration) *Command[BaseCommand] {
	return &Command[BaseCommand]{
		Name: "connect",
		Handler: func(args BaseCommand) error {
			typedArgs, ok := args.(*commandConnectArgs)
			if !ok {
				return InvalidCommandArgumentType
			}
			fmt.Printf("Target room: %s\n", *typedArgs.TargetRoomId)
			return nil
		},
		ParameterFactory: func() (BaseCommand, error) {
			flagSet := flag.NewFlagSet("connect", flag.ExitOnError)
			targetRoomId := flagSet.String("targetRoomId", "", "The target room ID")
			getHelp := flagSet.Bool("help", false, "Print this help")
			return &commandConnectArgs{
				FlagSet:      flagSet,
				GetHelp:      getHelp,
				TargetRoomId: targetRoomId,
			}, nil
		},

		//Dependencies
		Logger: logger,
		Client: client,
		Config: config,
	}
}

type commandConnectArgs struct {
	FlagSet      *flag.FlagSet
	GetHelp      *bool
	TargetRoomId *string
}

func (c *commandConnectArgs) GetFlagSet() *flag.FlagSet {
	return c.FlagSet
}

func (c *commandConnectArgs) IsHelp() bool {
	return *c.GetHelp
}
