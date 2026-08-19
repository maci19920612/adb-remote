package command

import (
	"adb-remote.maci.team/client/adb"
	"adb-remote.maci.team/client/config"
	"adb-remote.maci.team/client/controller"
	"adb-remote.maci.team/client/transportLayer"
	"context"
	"flag"
	"fmt"
	"log/slog"
)

func CreateShareCommand(
	logger *slog.Logger,
	client *transportLayer.Client,
	smartSocket adb.IAdbSmartSocket,
	config *config.ClientConfiguration,
) *Command[BaseCommand] {
	return &Command[BaseCommand]{
		Name: "share",
		Handler: func(args BaseCommand) error {
			typedArgs, ok := args.(*commandShareArgs)
			if !ok {
				return InvalidCommandArgumentType
			}
			fmt.Printf("Target device: %s\n", *typedArgs.TargetDevice)
			if err := controller.Handshake(client); err != nil {
				return err
			}
			acceptPrompt := controller.TTYAcceptPrompt
			if *typedArgs.AutoAccept {
				acceptPrompt = func(guestClientId string) (bool, error) {
					fmt.Printf("Auto-accepting the room join request (clientId:%s)\n", guestClientId)
					return true, nil
				}
			}
			return controller.JoinAsRoomOwner(context.Background(), client, smartSocket, *typedArgs.TargetDevice, acceptPrompt)
		},
		ParameterFactory: func() (BaseCommand, error) {
			flagSet := flag.NewFlagSet("share", flag.ExitOnError)
			targetDevice := flagSet.String("targetDevice", "", "The target device ID what you want to share")
			autoAccept := flagSet.Bool("yes", false, "Automatically accept every room join request instead of prompting on the TTY")
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
