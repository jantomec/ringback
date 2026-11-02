package main

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"sync"
)

// The pipe between the link process and its worker: the worker's stdin carries the hub's
// messages, its stdout the worker's, each as a frame: one byte of kind, 4 bytes of length
// (big-endian), then the message. Text and binary frames are the hub's messages as they
// came or go; control frames are JSON between the two processes, never relayed.

const (
	frameText    byte = 1
	frameBinary  byte = 2
	frameControl byte = 3
	maxFrame          = 4 << 20 // as the WebSocket's read limit
)

type frame struct {
	kind byte
	data []byte
}

func writeFrame(w io.Writer, f frame) error {
	b := make([]byte, 5+len(f.data))
	b[0] = f.kind
	binary.BigEndian.PutUint32(b[1:], uint32(len(f.data)))
	copy(b[5:], f.data)
	_, err := w.Write(b)
	return err
}

func readFrame(r *bufio.Reader) (frame, error) {
	var head [5]byte
	if _, err := io.ReadFull(r, head[:]); err != nil {
		return frame{}, err
	}
	n := binary.BigEndian.Uint32(head[1:])
	if head[0] < frameText || head[0] > frameControl || n > maxFrame {
		return frame{}, fmt.Errorf("a broken frame (kind %d, %d bytes)", head[0], n)
	}
	f := frame{kind: head[0], data: make([]byte, n)}
	_, err := io.ReadFull(r, f.data)
	return f, err
}

// queue is a first-in, first-out queue with no limit: pushing never waits, so the hub's link
// isn't held up while the worker loads its models, and nothing is dropped meanwhile.
type queue struct {
	mu     sync.Mutex
	more   *sync.Cond
	items  []frame
	closed bool
}

func newQueue() *queue {
	q := &queue{}
	q.more = sync.NewCond(&q.mu)
	return q
}

func (q *queue) push(f frame) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if !q.closed {
		q.items = append(q.items, f)
		q.more.Signal()
	}
}

// close drops what's waiting and ends pop.
func (q *queue) close() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.closed, q.items = true, nil
	q.more.Broadcast()
}

// pop waits for the next frame; false once the queue is closed.
func (q *queue) pop() (frame, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for len(q.items) == 0 && !q.closed {
		q.more.Wait()
	}
	if q.closed {
		return frame{}, false
	}
	f := q.items[0]
	q.items[0] = frame{}
	q.items = q.items[1:]
	return f, true
}
