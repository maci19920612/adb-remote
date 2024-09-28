package shared

type TransportRead interface {
	Read(data []byte) (int, error)
}

type TransportWrite interface {
	Write(data []byte) (int, error)
}
