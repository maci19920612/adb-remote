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

func CreateConnectCommand(
	logger *slog.Logger,
	client *transportLayer.Client,
	config *config.ClientConfiguration,
) *Command[BaseCommand] {
	return &Command[BaseCommand]{
		Name: "connect",
		Handler: func(args BaseCommand) error {
			typedArgs, ok := args.(*commandConnectArgs)
			if !ok {
				return InvalidCommandArgumentType
			}
			fmt.Printf("Target room: %s\n", *typedArgs.TargetRoomId)
			if err := controller.Handshake(client); err != nil {
				return err
			}
			return controller.JoinAsGuest(context.Background(), client, *typedArgs.TargetRoomId, *typedArgs.LocalPort)
		},
		ParameterFactory: func() (BaseCommand, error) {
			flagSet := flag.NewFlagSet("connect", flag.ExitOnError)
			targetRoomId := flagSet.String("targetRoomId", "", "The target room ID")
			localPort := flagSet.String("port", adb.DefaultProxyPort, "The local port to expose the remote device on, for \"adb connect\" to use")
			getHelp := flagSet.Bool("help", false, "Print this help")
			return &commandConnectArgs{
				FlagSet:      flagSet,
				GetHelp:      getHelp,
				TargetRoomId: targetRoomId,
				LocalPort:    localPort,
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
	LocalPort    *string
}

func (c *commandConnectArgs) GetFlagSet() *flag.FlagSet {
	return c.FlagSet
}

func (c *commandConnectArgs) IsHelp() bool {
	return *c.GetHelp
}
