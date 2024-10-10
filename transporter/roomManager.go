package main

type Room struct {
	clientId string
	ownerId  string
}

type RoomManager struct {
	rooms []Room
}

func NewRoomManager() *RoomManager {
	return &RoomManager{
		rooms: make([]Room, 2),
	}
}
