package egress

import (
	"net"
	"sync"
	"testing"
)

// blockingConn's Write and Read return only once Close is called, standing in
// for a transport goroutine still inside a syscall when the conn is closed.
type blockingConn struct {
	net.Conn
	entered chan struct{}
	closed  chan struct{}
	once    sync.Once
}

func (c *blockingConn) Write(p []byte) (int, error) {
	c.entered <- struct{}{}
	<-c.closed
	return len(p), nil
}
func (c *blockingConn) Read(p []byte) (int, error) {
	c.entered <- struct{}{}
	<-c.closed
	return len(p), nil
}
func (c *blockingConn) Close() error { c.once.Do(func() { close(c.closed) }); return nil }

func TestTrackedConnCloseCountsInFlightIO(t *testing.T) {
	for range 50 {
		got := make(chan [2]int64, 1)
		tc := &trackedConn{
			Conn:    &blockingConn{entered: make(chan struct{}, 2), closed: make(chan struct{})},
			onClose: func(up, down int64) { got <- [2]int64{up, down} },
		}
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); _, _ = tc.Write(make([]byte, 7)) }()
		go func() { defer wg.Done(); _, _ = tc.Read(make([]byte, 11)) }()
		<-tc.Conn.(*blockingConn).entered
		<-tc.Conn.(*blockingConn).entered
		if err := tc.Close(); err != nil {
			t.Fatal(err)
		}
		wg.Wait()
		if r := <-got; r != [2]int64{7, 11} {
			t.Fatalf("onClose(up, down) = %v, want [7 11]", r)
		}
	}
}
