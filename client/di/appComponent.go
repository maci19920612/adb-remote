package di

import (
	"adb-remote.maci.team/client/adb"
	"adb-remote.maci.team/client/command"
	"adb-remote.maci.team/client/config"
	"adb-remote.maci.team/client/identity"
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
	registerLogLevel(&cont)
	registerLogger(&cont)
	registerConfig(&cont)
	registerClient(&cont)
	registerSmartSocket(&cont)
	registerIdentity(&cont)
	registerCommands(&cont)
	return &cont
}

// logFilePath is where client logs go. Both `share` and `connect` run a
// full-screen TUI on stdout (see client/tui); a log line writing straight
// to stdout out-of-band would corrupt that rendering, so logs go to a file
// instead. pcapFilePath sits next to it, written only when -verbosity=debug
// enables packet capture (see transportLayer.Client.EnableDebugCapture).
var logFilePath = filepath.Join(os.TempDir(), "adb-remote-client.log")
var pcapFilePath = filepath.Join(os.TempDir(), "adb-remote-client.pcap")

// registerLogLevel provides the mutable *slog.LevelVar the logger is built
// with. It starts at the zero value (slog.LevelInfo); ParseCommand raises
// it to Debug once it has parsed a command's -verbosity flag, which is
// necessarily after this container (and so the logger) already exists.
func registerLogLevel(container *container.Container) {
	err := container.Singleton(func() *slog.LevelVar {
		return new(slog.LevelVar)
	})
	if err != nil {
		panic(err)
	}
}

func registerLogger(container *container.Container) {
	err := container.Singleton(func(logLevel *slog.LevelVar) *slog.Logger {
		var writer io.Writer
		logFile, err := os.OpenFile(logFilePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			writer = io.Discard
		} else {
			writer = logFile
		}
		return slog.New(prettyLogHandler.CreatePrettyHandler(writer, &slog.HandlerOptions{Level: logLevel}))
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

// registerIdentity loads (generating on first run) this installation's
// persistent identity keypair — see client/identity.
func registerIdentity(container *container.Container) {
	err := container.Singleton(func() (*identity.Identity, error) {
		path, err := identity.DefaultPath()
		if err != nil {
			return nil, err
		}
		return identity.Load(path)
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
		clientIdentity *identity.Identity,
		config *config.ClientConfiguration,
		logLevel *slog.LevelVar,
	) []*command.Command[command.BaseCommand] {
		return []*command.Command[command.BaseCommand]{
			command.CreateShareCommand(logger, client, smartSocket, clientIdentity, config, logLevel, pcapFilePath),
			command.CreateConnectCommand(logger, client, smartSocket, clientIdentity, config, logLevel, pcapFilePath),
		}
	})
	if err != nil {
		panic(err)
	}
}
