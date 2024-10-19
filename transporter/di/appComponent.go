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
	registerLogger(&cont)
	registerConfiguration(&cont)
	registerConnectionManager(&cont)
	registerRoomManager(&cont)
	return cont
}

func registerLogger(container *container.Container) {
	err := container.Singleton(func() *slog.Logger {
		//handlerOptions := &slog.HandlerOptions{
		//	AddSource: false,
		//}
		//handler := slog.New(&prettyLogHandler.Handler{})
		//handler := slog.NewTextHandler(os.Stdout, handlerOptions)
		return slog.New(prettyLogHandler.CreatePrettyHandler(&slog.HandlerOptions{}))
	})
	if err != nil {
		panic(err)
	}
}

func registerConfiguration(container *container.Container) {
	workingDirectory, err := os.Getwd()
	if err != nil {
		panic(err)
	}

	configInstance := config.CreateConfig(path.Join(workingDirectory, ConfigFileName))
	err = container.Singleton(func() *config.AppConfiguration {
		return configInstance
	})

	if err != nil {
		panic(err)
	}
}

func registerConnectionManager(container *container.Container) {
	err := container.Singleton(func(config *config.AppConfiguration, logger *slog.Logger) *connectionManager.ConnectionManager {
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
