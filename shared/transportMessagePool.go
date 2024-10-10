package shared

import (
	"adb-remote.maci.team/shared/protocol"
	"sync"
)

const poolSizeInitial = 10
const poolSizeMax = 100

type TransportMessagePool struct {
	mutex     *sync.Mutex
	container []*protocol.TransporterMessage
	length    int
}

func CreateTransportMessagePool() *TransportMessagePool {
	return &TransportMessagePool{
		mutex:     new(sync.Mutex),
		container: make([]*protocol.TransporterMessage, poolSizeInitial),
		length:    0,
	}
}

func (pool *TransportMessagePool) Obtain() *protocol.TransporterMessage {
	pool.mutex.Lock()
	defer pool.mutex.Unlock()
	if pool.length <= 0 {
		return protocol.CreateTransporterMessage()
	} else {
		pool.length--
		transporterMessage := pool.container[pool.length]
		pool.container[pool.length] = nil
		return transporterMessage
	}
}

func (pool *TransportMessagePool) Release(transporterMessage *protocol.TransporterMessage) bool {
	pool.mutex.Lock()
	defer pool.mutex.Unlock()

	if pool.length >= poolSizeMax {
		return false
	}
	pool.container[pool.length] = transporterMessage
	pool.length++
	return true
}
