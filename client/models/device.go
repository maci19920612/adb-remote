package models

const (
	TypeDevice       string = "device"
	TypeHost         string = "host"
	TypeUnknown      string = "unknown"
	TypeDisconnected string = "offline"
)

type Device struct {
	Id   string
	Type string
}
