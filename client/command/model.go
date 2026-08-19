package command

import (
	"adb-remote.maci.team/client/adb"
	"adb-remote.maci.team/client/config"
	"adb-remote.maci.team/client/transportLayer"
	"flag"
	"log/slog"
)

type CommandHandler[T BaseCommand] func(args T) error
type FlagSetFactory[T BaseCommand] func() (T, error)

type Command[T BaseCommand] struct {
	Name             string
	Handler          CommandHandler[T]
	ParameterFactory FlagSetFactory[T]

	//Dependencies
	Logger      *slog.Logger
	Client      *transportLayer.Client
	Config      *config.ClientConfiguration
	SmartSocket adb.IAdbSmartSocket

	// LogLevel is adjusted by ParseCommand once it knows the parsed
	// -verbosity flag, so debug logging takes effect even though the
	// *slog.Logger itself was already built at DI-container construction
	// time, before any flags were parsed.
	LogLevel *slog.LevelVar
	// PcapPath is where debug-verbosity packet capture is written; see
	// transportLayer.Client.EnableDebugCapture.
	PcapPath string
}

type BaseCommand interface {
	GetFlagSet() *flag.FlagSet
	IsHelp() bool
	// Verbosity returns the parsed -verbosity flag value ("default" or
	// "debug"), so ParseCommand can apply it before starting the session.
	Verbosity() string
}
