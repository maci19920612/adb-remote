package command

import (
	"adb-remote.maci.team/client/config"
	"adb-remote.maci.team/client/controller"
	"adb-remote.maci.team/client/transportLayer"
	"flag"
	"fmt"
	"log/slog"
)

func CreateShareCommand(logger *slog.Logger, client *transportLayer.Client, config *config.ClientConfiguration) *Command[BaseCommand] {
	return &Command[BaseCommand]{
		Name: "share",
		Handler: func(args BaseCommand) error {
			typedArgs, ok := args.(*commandShareArgs)
			if !ok {
				return InvalidCommandArgumentType
			}
			fmt.Printf("Target device: %s\n", *typedArgs.TargetDevice)
			controller.Handshake(client)
			controller.JoinAsRoomOwner(client, *typedArgs.TargetDevice)
			return nil
		},
		ParameterFactory: func() (BaseCommand, error) {
			flagSet := flag.NewFlagSet("share", flag.ExitOnError)
			targetDevice := flagSet.String("targetDevice", "", "The target device ID what you want to share")
			getHelp := flagSet.Bool("help", false, "Print this help")
			return &commandShareArgs{
				FlagSet:      flagSet,
				GetHelp:      getHelp,
				TargetDevice: targetDevice,
			}, nil
		},

		//Dependencies
		Logger: logger,
		Client: client,
		Config: config,
	}
}

type commandShareArgs struct {
	FlagSet      *flag.FlagSet
	GetHelp      *bool
	TargetDevice *string
}

func (c *commandShareArgs) GetFlagSet() *flag.FlagSet {
	return c.FlagSet
}

func (c *commandShareArgs) IsHelp() bool {
	return *c.GetHelp
}
