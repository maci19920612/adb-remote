package main

import (
	"adb-remote.maci.team/client/models"
	"adb-remote.maci.team/client/transportLayer"
	"adb-remote.maci.team/shared/protocol"
	"fmt"
	"log"
)

var global_buffer = make([]byte, 1024*1024)
var adb_message = new(models.AdbMessage)

func setupLogger() {
	logger := log.Default()
	logger.SetFlags(log.Lshortfile | log.Ldate | log.Ltime)
}

func main() {
	//setupLogger()
	//smartSocket := CreateDefaultHost()
	//deviceList, err := smartSocket.DeviceList()
	//if err != nil {
	//	panic(err)
	//}
	//fmt.Println(deviceList)
	//
	//return
	//
	//listener, err := net.Listen("tcp", "127.0.0.1:1234")
	//if err != nil {
	//	fmt.Println(err)
	//	panic("Can't start the TCP server")
	//}
	//fmt.Println("Listening on port 1234")
	//for {
	//	connection, err := listener.Accept()
	//	if err != nil {
	//		fmt.Printf("Connection accept failed: %s", err)
	//		continue
	//	}
	//	fmt.Println("Connection accepted")
	//
	//	message_header_buffer := global_buffer[0:models.HeaderSize]
	//	length, err := connection.Read(message_header_buffer) //TODO: Handle the case when we don't read enough data from the source
	//	if err != nil {
	//		fmt.Println("Error during the connection read: ", err)
	//		connection.Close()
	//		continue
	//	}
	//	if length < models.HeaderSize {
	//		fmt.Printf("Invalid length read from the network:")
	//	}
	//	adb_message.ReadHeader(message_header_buffer)
	//	length, err = connection.Read(global_buffer[0:adb_message.DataLength]) //TODO: Handle the case when we don't read enough data from the source
	//	if err != nil {
	//		fmt.Println("Invalid length read from the network:", err)
	//		connection.Close()
	//		continue
	//	}
	//	adb_message.ReadData(global_buffer[0:adb_message.DataLength])
	//	adb_message.Dump(models.MessageDirectionIn)
	//	version := adb_message.Arg1
	//	maxSize := adb_message.Arg2
	//
	//	adb_message.SetHeader(models.CommandConnect, version, maxSize, []byte("device:wrapped-something-something"))
	//	length, err = adb_message.Write(global_buffer)
	//	if err != nil {
	//		fmt.Println("Error during the ADB message write to the intermediate array")
	//		connection.Close()
	//		continue
	//	}
	//	adb_message.Dump(models.MessageDirectionOut)
	//	connection.Write(global_buffer[0:length])

	client, err := transportLayer.CreateClient("127.0.0.1:3000")
	if err != nil {
		panic(err)
	}

	if err := client.SendConnect(); err != nil {
		panic(err)
	}

	connectResponseMessage := <-client.MessageChannel
	if err := protocol.ExpectCommand(connectResponseMessage, protocol.CommandConnect|protocol.CommandResponseMask); err != nil {
		panic(err)
	}
	response, err := connectResponseMessage.GetPayloadConnectResponse()
	if err != nil {
		panic(err)
	}
	fmt.Printf("YOUR CLIENT ID: %s\n", response.ClientId)
	client.Release(connectResponseMessage)

	if err := client.SendCreateRoom(); err != nil {
		panic(err)
	}
	createRoomResponseMessage := <-client.MessageChannel
	if err := protocol.ExpectCommand(createRoomResponseMessage, protocol.CommandCreateRoom|protocol.CommandResponseMask); err != nil {
		panic(err)
	}
	createRoomResponsePayload, err := createRoomResponseMessage.GetPayloadCreateRoomResponse()
	if err != nil {
		panic(err)
	}
	fmt.Printf("ROOM CREATED, ROOM ID: %s\n", createRoomResponsePayload.RoomId)
}
