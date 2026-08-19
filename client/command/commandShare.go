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
	"time"
)

// DefaultSessionTimeoutMinutes is how long a shared room stays open before
// it's automatically closed, unless overridden with -sessionTimeout.
const DefaultSessionTimeoutMinutes = 120

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
			// A zero or negative timeout (including the documented -1
			// sentinel) disables the timer entirely.
			sessionTimeout := time.Duration(*typedArgs.SessionTimeoutMinutes) * time.Minute
			return tui.RunShare(context.Background(), client, smartSocket, ownerIdentity, *typedArgs.TargetDevice, *typedArgs.AutoAccept, sessionTimeout)
		},
		ParameterFactory: func() (BaseCommand, error) {
			flagSet := flag.NewFlagSet("share", flag.ExitOnError)
			targetDevice := flagSet.String("targetDevice", "", "The device ID to share; skips the device picker if set")
			autoAccept := flagSet.Bool("yes", false, "Automatically accept every room join request instead of prompting")
			sessionTimeoutMinutes := flagSet.Int("sessionTimeout", DefaultSessionTimeoutMinutes, "Minutes before the room is automatically closed; -1 disables the timeout")
			getHelp := flagSet.Bool("help", false, "Print this help")
			return &commandShareArgs{
				FlagSet:               flagSet,
				GetHelp:               getHelp,
				TargetDevice:          targetDevice,
				AutoAccept:            autoAccept,
				SessionTimeoutMinutes: sessionTimeoutMinutes,
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
	FlagSet               *flag.FlagSet
	GetHelp               *bool
	TargetDevice          *string
	AutoAccept            *bool
	SessionTimeoutMinutes *int
}

func (c *commandShareArgs) GetFlagSet() *flag.FlagSet {
	return c.FlagSet
}

func (c *commandShareArgs) IsHelp() bool {
	return *c.GetHelp
}
