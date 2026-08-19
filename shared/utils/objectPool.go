package utils

import (
	"errors"
	"sync"
)

const poolSizeMax = 100

type ObjectPoolFactory[T any] func() *T

type ObjectPool[T any] struct {
	mutex     *sync.Mutex
	container []*DisposableObjectContainer[T]
	factory   ObjectPoolFactory[T]
	length    int
}

func NewObjectPool[T any](factory ObjectPoolFactory[T]) *ObjectPool[T] {
	return &ObjectPool[T]{
		mutex:     new(sync.Mutex),
		container: make([]*DisposableObjectContainer[T], poolSizeMax),
		factory:   factory,
		length:    0,
	}
}

func (pool *ObjectPool[T]) Obtain() *DisposableObjectContainer[T] {
	pool.mutex.Lock()
	defer pool.mutex.Unlock()
	if pool.length <= 0 {
		return newDisposableObjectContainer(pool, pool.factory())
	} else {
		pool.length--
		cachedObject := pool.container[pool.length]
		pool.container[pool.length] = nil
		cachedObject.reset()
		return cachedObject
	}
}

func (pool *ObjectPool[T]) release(container *DisposableObjectContainer[T]) bool {
	pool.mutex.Lock()
	defer pool.mutex.Unlock()

	if pool.length >= poolSizeMax {
		return false
	}
	pool.container[pool.length] = container
	pool.length++
	return true
}

type DisposableObjectContainer[T any] struct {
	parent  *ObjectPool[T]
	data    *T
	current uint32
	target  uint32
	mutex   *sync.Mutex
}

var ErrObjectAlreadyDisposed = errors.New("object already disposed")

func newDisposableObjectContainer[T any](parent *ObjectPool[T], data *T) *DisposableObjectContainer[T] {
	return &DisposableObjectContainer[T]{
		parent:  parent,
		data:    data,
		current: 0,
		target:  1,
		mutex:   new(sync.Mutex),
	}
}

// reset restores a container pulled back out of the pool to a fresh,
// single-owner disposal state. Must only be called while the container is
// not referenced by anyone else, i.e. right after it is popped from the
// pool's free list inside Obtain.
func (d *DisposableObjectContainer[T]) reset() {
	d.mutex.Lock()
	defer d.mutex.Unlock()
	d.current = 0
	d.target = 1
}

func (d *DisposableObjectContainer[T]) Data() (*T, error) {
	d.mutex.Lock()
	defer d.mutex.Unlock()
	if d.current >= d.target {
		return nil, ErrObjectAlreadyDisposed
	}
	return d.data, nil
}

func (d *DisposableObjectContainer[T]) UpdateCounter(newTarget uint32) error {
	d.mutex.Lock()
	defer d.mutex.Unlock()
	if d.current >= d.target {
		return ErrObjectAlreadyDisposed
	}
	d.target = newTarget
	return nil
}

func (d *DisposableObjectContainer[T]) Dispose() error {
	d.mutex.Lock()
	defer d.mutex.Unlock()

	if d.current >= d.target {
		return ErrObjectAlreadyDisposed
	}

	d.current++
	if d.current >= d.target {
		d.parent.release(d)
	}
	return nil
}
