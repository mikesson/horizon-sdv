// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     https://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package task

// Waker carries one process-local signal: work reached the queue, so a parked
// worker can stop waiting out its poll delay. It exists because the producer
// path that matters most (a task insert folded into the caller's own
// transaction) never passes through the queue's own commit, so nothing else
// tells the pool a commit happened.
//
// It is lossy on purpose. Wake never blocks, drops the signal when the buffer
// is full, and a nil Waker discards every signal and never fires. Delivery is
// the poll's guarantee, not this one's: a dropped, missing or entirely
// disabled wake costs one poll interval and never a task.
type Waker struct {
	ch chan struct{}
}

// NewWaker returns a waker buffering n signals. Size it to the number of
// workers that could be parked at once, so one burst of enqueues wakes more
// than one of them. n below 1 buffers 1.
func NewWaker(n int) *Waker {
	return &Waker{ch: make(chan struct{}, max(n, 1))}
}

// Wake signals that the queue may now have claimable work. Safe on a nil
// receiver and from any goroutine.
func (w *Waker) Wake() {
	if w == nil {
		return
	}
	select {
	case w.ch <- struct{}{}:
	default: // A parked worker is already owed a look; one signal is enough.
	}
}

// C is the channel a parked worker selects on. It is nil for a nil Waker,
// which never fires. The pool then discovers work on its poll floor alone.
func (w *Waker) C() <-chan struct{} {
	if w == nil {
		return nil
	}
	return w.ch
}
