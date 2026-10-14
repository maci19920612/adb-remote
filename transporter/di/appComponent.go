package di

import (
	"adb-remote.maci.team/shared/prettyLogHandler"
	"adb-remote.maci.team/transporter/config"
	"adb-remote.maci.team/transporter/manager/connectionManager"
	"adb-remote.maci.team/transporter/manager/roomManager"
	"github.com/golobby/container/v3"
	"log/slog"
	"os"
	"path"
)

const ConfigFileName = "config.json"

func CreateContainer() container.Container {
	cont := container.New()
	// The configuration comes first: the logger's level depends on it.
	registerConfiguration(&cont)
	registerLogger(&cont)
	registerConnectionManager(&cont)
	registerRoomManager(&cont)
	return cont
}

func registerLogger(container *container.Container) {
	err := container.Singleton(newLogger)
	if err != nil {
		panic(err)
	}
}

// newLogger logs to stdout at Info level, or at Debug level with debug
// verbosity.
func newLogger(configuration *config.TransporterConfiguration) *slog.Logger {
	level := slog.LevelInfo
	if configuration.IsDebug() {
		level = slog.LevelDebug
	}
	return slog.New(prettyLogHandler.CreatePrettyHandler(os.Stdout, &slog.HandlerOptions{Level: level}))
}

func registerConfiguration(container *container.Container) {
	workingDirectory, err := os.Getwd()
	if err != nil {
		panic(err)
	}

	configInstance, err := config.CreateConfig(path.Join(workingDirectory, ConfigFileName))
	if err != nil {
		panic(err)
	}
	err = container.Singleton(func() *config.TransporterConfiguration {
		return configInstance
	})

	if err != nil {
		panic(err)
	}
}

func registerConnectionManager(container *container.Container) {
	err := container.Singleton(func(config *config.TransporterConfiguration, logger *slog.Logger) *connectionManager.ConnectionManager {
		return connectionManager.CreateConnectionManager(config, logger)
	})
	if err != nil {
		panic(err)
	}
}

func registerRoomManager(container *container.Container) {
	err := container.Singleton(
		func(logger *slog.Logger, connectionManager *connectionManager.ConnectionManager) *roomManager.RoomManager {
			return roomManager.CreateRoomManager(connectionManager, logger)
		},
	)
	if err != nil {
		panic(err)
	}
}
