package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var errTestShuttingDown = errors.New("test owner is shutting down")

func newTestOwner() *Owner {
	return NewOwner(errTestShuttingDown, func(active int, err error) error {
		return fmt.Errorf("test owner: %d unit(s) still active at the shutdown deadline: %w", active, err)
	})
}

func TestOwner(t *testing.T) {
	t.Run("admit after shutdown returns the given error", func(t *testing.T) {
		o := newTestOwner()
		require.NoError(t, o.Shutdown(context.Background()))
		release, err := o.Admit()
		assert.Nil(t, release)
		assert.ErrorIs(t, err, errTestShuttingDown)
		assert.Error(t, o.Context().Err(), "the owned context ends with the shutdown")
	})

	t.Run("release twice counts once", func(t *testing.T) {
		o := newTestOwner()
		first, err := o.Admit()
		require.NoError(t, err)
		second, err := o.Admit()
		require.NoError(t, err)
		assert.Equal(t, 2, o.Active())
		first()
		first()
		assert.Equal(t, 1, o.Active())
		second()
		assert.Equal(t, 0, o.Active())
	})

	t.Run("shutdown waits for release", func(t *testing.T) {
		o := newTestOwner()
		release, err := o.Admit()
		require.NoError(t, err)
		result := make(chan error, 1)
		go func() { result <- o.Shutdown(context.Background()) }()
		select {
		case <-o.Context().Done():
		case <-time.After(waitBound):
			t.Fatal("shutdown did not cancel the owned context")
		}
		select {
		case err := <-result:
			t.Fatalf("shutdown returned %v before the release", err)
		default:
		}
		release()
		select {
		case err := <-result:
			assert.NoError(t, err)
		case <-time.After(waitBound):
			t.Fatal("shutdown did not return after the release")
		}
	})

	t.Run("the deadline error carries the count", func(t *testing.T) {
		o := newTestOwner()
		first, err := o.Admit()
		require.NoError(t, err)
		second, err := o.Admit()
		require.NoError(t, err)
		ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
		defer cancel()
		err = o.Shutdown(ctx)
		require.Error(t, err)
		assert.ErrorIs(t, err, context.DeadlineExceeded)
		assert.Equal(t, "test owner: 2 unit(s) still active at the shutdown deadline: context deadline exceeded", err.Error())
		first()
		second()
		require.NoError(t, o.Shutdown(context.Background()))
	})

	t.Run("a drained owner on an ended context returns nil every time", func(t *testing.T) {
		o := newTestOwner()
		release, err := o.Admit()
		require.NoError(t, err)
		release()
		require.NoError(t, o.Shutdown(context.Background()))
		ended, cancel := context.WithCancel(context.Background())
		cancel()
		for i := 0; i < 100; i++ {
			require.NoError(t, o.Shutdown(ended), "call %d", i)
		}
	})

	t.Run("concurrent admit and shutdown", func(t *testing.T) {
		for round := 0; round < 50; round++ {
			o := newTestOwner()
			var admitted, refused sync.WaitGroup
			var mu sync.Mutex
			releases := []func(){}
			var refusals int
			start := make(chan struct{})
			for i := 0; i < 8; i++ {
				admitted.Add(1)
				go func() {
					defer admitted.Done()
					<-start
					release, err := o.Admit()
					mu.Lock()
					defer mu.Unlock()
					if err != nil {
						assert.ErrorIs(t, err, errTestShuttingDown)
						refusals++
						return
					}
					releases = append(releases, release)
				}()
			}
			result := make(chan error, 1)
			refused.Add(1)
			go func() {
				defer refused.Done()
				<-start
				ctx, cancel := context.WithTimeout(context.Background(), waitBound)
				defer cancel()
				result <- o.Shutdown(ctx)
			}()
			close(start)
			admitted.Wait()
			mu.Lock()
			assert.Equal(t, 8, len(releases)+refusals)
			assert.Equal(t, len(releases), o.Active(), "every admitted unit is counted, every refused one is not")
			for _, release := range releases {
				release()
			}
			mu.Unlock()
			refused.Wait()
			assert.NoError(t, <-result)
			_, err := o.Admit()
			assert.ErrorIs(t, err, errTestShuttingDown)
		}
	})
}
