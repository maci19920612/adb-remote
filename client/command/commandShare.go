package command

import (
	"adb-remote.maci.team/client/adb"
	"adb-remote.maci.team/client/config"
	"adb-remote.maci.team/client/identity"
	"adb-remote.maci.team/client/transportLayer"
	"adb-remote.maci.team/client/tui"
	"context"
	"flag"
	"log/slog"
)

func CreateShareCommand(
	logger *slog.Logger,
	client *transportLayer.Client,
	smartSocket adb.IAdbSmartSocket,
	ownerIdentity *identity.Identity,
	config *config.ClientConfiguration,
) *Command[BaseCommand] {
	return &Command[BaseCommand]{
		Name: "share",
		Handler: func(args BaseCommand) error {
			typedArgs, ok := args.(*commandShareArgs)
			if !ok {
				return InvalidCommandArgumentType
			}
			return tui.RunShare(context.Background(), client, smartSocket, ownerIdentity, *typedArgs.TargetDevice, *typedArgs.AutoAccept)
		},
		ParameterFactory: func() (BaseCommand, error) {
			flagSet := flag.NewFlagSet("share", flag.ExitOnError)
			targetDevice := flagSet.String("targetDevice", "", "The device ID to share; skips the device picker if set")
			autoAccept := flagSet.Bool("yes", false, "Automatically accept every room join request instead of prompting")
			getHelp := flagSet.Bool("help", false, "Print this help")
			return &commandShareArgs{
				FlagSet:      flagSet,
				GetHelp:      getHelp,
				TargetDevice: targetDevice,
				AutoAccept:   autoAccept,
			}, nil
		},

		//Dependencies
		Logger:      logger,
		Client:      client,
		Config:      config,
		SmartSocket: smartSocket,
	}
}

type commandShareArgs struct {
	FlagSet      *flag.FlagSet
	GetHelp      *bool
	TargetDevice *string
	AutoAccept   *bool
}

func (c *commandShareArgs) GetFlagSet() *flag.FlagSet {
	return c.FlagSet
}

func (c *commandShareArgs) IsHelp() bool {
	return *c.GetHelp
}
