package utils

import (
	"sync"
	"testing"
)

type poolItem struct {
	value int
}

func newTestPool() (*ObjectPool[poolItem], *int) {
	created := 0
	factory := func() *poolItem {
		created++
		return &poolItem{value: created}
	}
	return NewObjectPool(factory), &created
}

func TestObtainCreatesNewObjectWhenPoolEmpty(t *testing.T) {
	pool, created := newTestPool()
	container := pool.Obtain()
	data, err := container.Data()
	if err != nil {
		t.Fatalf("Data() failed: %s", err)
	}
	if data.value != 1 {
		t.Fatalf("expected the factory-created value 1, got %d", data.value)
	}
	if *created != 1 {
		t.Fatalf("expected the factory to be called once, called %d times", *created)
	}
}

func TestDisposeReturnsObjectToPoolForReuse(t *testing.T) {
	pool, created := newTestPool()
	container := pool.Obtain()
	if err := container.Dispose(); err != nil {
		t.Fatalf("Dispose failed: %s", err)
	}

	reused := pool.Obtain()
	if *created != 1 {
		t.Fatalf("expected the disposed object to be reused instead of creating a new one, factory called %d times", *created)
	}
	if _, err := reused.Data(); err != nil {
		t.Fatalf("a reused container must be usable again, got: %s", err)
	}
}

func TestDataFailsAfterDispose(t *testing.T) {
	pool, _ := newTestPool()
	container := pool.Obtain()
	if err := container.Dispose(); err != nil {
		t.Fatalf("Dispose failed: %s", err)
	}
	if _, err := container.Data(); err != ErrObjectAlreadyDisposed {
		t.Fatalf("expected ErrObjectAlreadyDisposed, got %v", err)
	}
}

func TestDoubleDisposeFails(t *testing.T) {
	pool, _ := newTestPool()
	container := pool.Obtain()
	if err := container.Dispose(); err != nil {
		t.Fatalf("first Dispose failed: %s", err)
	}
	if err := container.Dispose(); err != ErrObjectAlreadyDisposed {
		t.Fatalf("expected the second Dispose to fail with ErrObjectAlreadyDisposed, got %v", err)
	}
}

// TestUpdateCounterSupportsMultiplexing exercises the reference-counted
// disposal path: a message handed out to several concurrent consumers should
// only be returned to the pool once every consumer has disposed of it.
func TestUpdateCounterSupportsMultiplexing(t *testing.T) {
	pool, created := newTestPool()
	container := pool.Obtain()
	if err := container.UpdateCounter(3); err != nil {
		t.Fatalf("UpdateCounter failed: %s", err)
	}

	for i := 0; i < 2; i++ {
		if err := container.Dispose(); err != nil {
			t.Fatalf("Dispose #%d failed: %s", i, err)
		}
		if _, err := container.Data(); err != nil {
			t.Fatalf("Data() should still succeed before the target count is reached, got: %s", err)
		}
	}

	if err := container.Dispose(); err != nil {
		t.Fatalf("final Dispose failed: %s", err)
	}
	if _, err := container.Data(); err != ErrObjectAlreadyDisposed {
		t.Fatalf("expected the container to be fully disposed once the target count is reached")
	}

	reused := pool.Obtain()
	if *created != 1 {
		t.Fatalf("expected the multiplexed object to be reused, factory called %d times", *created)
	}
	if reused.target != 1 {
		t.Fatalf("expected a reused container's target counter to reset to 1, got %d", reused.target)
	}
}

func TestPoolGrowsPastInitialCapacity(t *testing.T) {
	pool, created := newTestPool()
	const count = 50
	containers := make([]*DisposableObjectContainer[poolItem], count)
	for i := range containers {
		containers[i] = pool.Obtain()
	}
	if *created != count {
		t.Fatalf("expected %d objects to be created, got %d", count, *created)
	}
	for _, container := range containers {
		if err := container.Dispose(); err != nil {
			t.Fatalf("Dispose failed: %s", err)
		}
	}

	// All 50 released objects must be reusable without the factory running again.
	for i := 0; i < count; i++ {
		pool.Obtain()
	}
	if *created != count {
		t.Fatalf("expected all %d disposed objects to be reused, factory called %d times", count, *created)
	}
}

func TestPoolDropsObjectsBeyondMaxSize(t *testing.T) {
	pool, created := newTestPool()
	containers := make([]*DisposableObjectContainer[poolItem], poolSizeMax+5)
	for i := range containers {
		containers[i] = pool.Obtain()
	}
	for _, container := range containers {
		if err := container.Dispose(); err != nil {
			t.Fatalf("Dispose failed: %s", err)
		}
	}
	if pool.length != poolSizeMax {
		t.Fatalf("expected the pool to cap its free list at %d, got %d", poolSizeMax, pool.length)
	}
	_ = created
}

func TestObtainDisposeConcurrentUse(t *testing.T) {
	pool, _ := newTestPool()
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c := pool.Obtain()
			if _, err := c.Data(); err != nil {
				t.Errorf("Data() failed: %s", err)
			}
			if err := c.Dispose(); err != nil {
				t.Errorf("Dispose failed: %s", err)
			}
		}()
	}
	wg.Wait()
}
