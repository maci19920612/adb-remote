package di

import (
	"adb-remote.maci.team/transporter/config"
	"adb-remote.maci.team/transporter/manager/connectionManager"
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
	return cont
}

func registerLogger(container *container.Container) {
	err := container.Singleton(func() *slog.Logger {
		handlerOptions := &slog.HandlerOptions{
			AddSource: true,
		}
		handler := slog.NewTextHandler(os.Stdout, handlerOptions)
		return slog.New(handler)
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
	err := container.Singleton(func(config *config.AppConfiguration, logger *slog.Logger) connectionManager.IConnectionManager {
		return connectionManager.CreateConnectionManager(config, logger)
	})
	if err != nil {
		panic(err)
	}
}
