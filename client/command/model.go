package command

import (
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
	Logger *slog.Logger
	Client *transportLayer.Client
	Config *config.ClientConfiguration
}

type BaseCommand interface {
	GetFlagSet() *flag.FlagSet
	IsHelp() bool
}
