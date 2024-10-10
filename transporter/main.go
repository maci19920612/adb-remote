package main

import (
	"log"
)

type TestJson struct {
	ServerType    string `json:serverType`
	ServerAddress string `json:serverAddress`
}

func main() {

	log.SetFlags(log.Ldate | log.Lshortfile | log.Ltime)
	StartServer(ServerConfiguration{
		ServerType:    "tcp",
		ServerAddress: "0.0.0.0:1234",
	})
}
