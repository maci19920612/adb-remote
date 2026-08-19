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

func CreateConnectCommand(
	logger *slog.Logger,
	client *transportLayer.Client,
	smartSocket adb.IAdbSmartSocket,
	guestIdentity *identity.Identity,
	config *config.ClientConfiguration,
	logLevel *slog.LevelVar,
	pcapPath string,
) *Command[BaseCommand] {
	return &Command[BaseCommand]{
		Name: "connect",
		Handler: func(args BaseCommand) error {
			typedArgs, ok := args.(*commandConnectArgs)
			if !ok {
				return InvalidCommandArgumentType
			}
			return tui.RunConnect(context.Background(), client, smartSocket, guestIdentity, *typedArgs.TargetRoomId, *typedArgs.LocalPort)
		},
		ParameterFactory: func() (BaseCommand, error) {
			flagSet := flag.NewFlagSet("connect", flag.ExitOnError)
			targetRoomId := flagSet.String("targetRoomId", "", "The target room ID")
			localPort := flagSet.String("port", adb.DefaultProxyPort, "The local port to expose the remote device on, for \"adb connect\" to use")
			verbosity := RegisterVerbosityFlag(flagSet)
			getHelp := flagSet.Bool("help", false, "Print this help")
			return &commandConnectArgs{
				FlagSet:       flagSet,
				GetHelp:       getHelp,
				TargetRoomId:  targetRoomId,
				LocalPort:     localPort,
				VerbosityFlag: verbosity,
			}, nil
		},

		//Dependencies
		Logger:      logger,
		Client:      client,
		Config:      config,
		SmartSocket: smartSocket,
		LogLevel:    logLevel,
		PcapPath:    pcapPath,
	}
}

type commandConnectArgs struct {
	FlagSet       *flag.FlagSet
	GetHelp       *bool
	TargetRoomId  *string
	LocalPort     *string
	VerbosityFlag *string
}

func (c *commandConnectArgs) GetFlagSet() *flag.FlagSet {
	return c.FlagSet
}

func (c *commandConnectArgs) IsHelp() bool {
	return *c.GetHelp
}

func (c *commandConnectArgs) Verbosity() string {
	return *c.VerbosityFlag
}
