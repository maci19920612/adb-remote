package main

import (
	"adb-remote.maci.team/transporter/di"
	"adb-remote.maci.team/transporter/manager/connectionManager"
	"log"
)

func main() {

	log.SetFlags(log.Ldate | log.Ltime)
	container := di.CreateContainer()
	err := container.Call(func(connectionManager *connectionManager.ConnectionManager) {
		err := connectionManager.StartServer()
		if err != nil {
			panic(err)
		}
	})

	if err != nil {
		panic(err)
	}
}
