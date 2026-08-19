package di

import (
	"adb-remote.maci.team/client/adb"
	"adb-remote.maci.team/client/command"
	"adb-remote.maci.team/client/config"
	"adb-remote.maci.team/client/transportLayer"
	"adb-remote.maci.team/shared/prettyLogHandler"
	"github.com/golobby/container/v3"
	"io"
	"log/slog"
	"os"
	"path/filepath"
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

// logFilePath is where client logs go. Both `share` and `connect` run a
// full-screen TUI on stdout (see client/tui); a log line writing straight
// to stdout out-of-band would corrupt that rendering, so logs go to a file
// instead.
var logFilePath = filepath.Join(os.TempDir(), "adb-remote-client.log")

func registerLogger(container *container.Container) {
	err := container.Singleton(func() *slog.Logger {
		var writer io.Writer
		logFile, err := os.OpenFile(logFilePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			writer = io.Discard
		} else {
			writer = logFile
		}
		return slog.New(prettyLogHandler.CreatePrettyHandler(writer, &slog.HandlerOptions{}))
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
