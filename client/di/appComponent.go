package di

import (
	"adb-remote.maci.team/client/adb"
	"adb-remote.maci.team/client/command"
	"adb-remote.maci.team/client/config"
	"adb-remote.maci.team/client/transportLayer"
	"adb-remote.maci.team/shared/prettyLogHandler"
	"github.com/golobby/container/v3"
	"log/slog"
)

func CreateContainer() *container.Container {
	cont := container.New()
	registerLogger(&cont)
	registerConfig(&cont)
	registerClient(&cont)
	registerSmartSocket(&cont)
	registerCommands(&cont)
	return &cont
}

func registerLogger(container *container.Container) {
	err := container.Singleton(func() *slog.Logger {
		return slog.New(prettyLogHandler.CreatePrettyHandler(&slog.HandlerOptions{}))
	})
	if err != nil {
		panic(err)
	}
}

func registerConfig(container *container.Container) {
	err := container.Singleton(func() (*config.ClientConfiguration, error) {
		return config.CreateConfig()
	})
	if err != nil {
		panic(err)
	}
}

func registerClient(container *container.Container) {
	err := container.Singleton(func(config *config.ClientConfiguration, logger *slog.Logger) (*transportLayer.Client, error) {
		return transportLayer.CreateClient(logger, config)
	})
	if err != nil {
		panic(err)
	}
}

func registerSmartSocket(container *container.Container) {
	err := container.Singleton(func(logger *slog.Logger) adb.IAdbSmartSocket {
		return adb.NewAdbSmartSocket(logger)
	})
	if err != nil {
		panic(err)
	}
}

func registerCommands(container *container.Container) {
	err := container.Singleton(func(
		logger *slog.Logger,
		client *transportLayer.Client,
		smartSocket adb.IAdbSmartSocket,
		config *config.ClientConfiguration,
	) []*command.Command[command.BaseCommand] {
		return []*command.Command[command.BaseCommand]{
			command.CreateShareCommand(logger, client, smartSocket, config),
			command.CreateConnectCommand(logger, client, config),
		}
	})
	if err != nil {
		panic(err)
	}
}
