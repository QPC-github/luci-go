// Copyright 2015 The LUCI Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package promise

import (
	"context"
	"errors"
	"sync"
)

var (
	// ErrNoData is an error returned when a request completes without available data.
	ErrNoData = errors.New("promise: No Data")
)

// Generator is the Promise's generator type.
type Generator func(context.Context) (any, error)

// Promise is a promise structure with goroutine-safe methods that is
// responsible for owning a single piece of data. Promises have multiple readers
// and a single writer.
//
// Readers will retrieve the Promise's data via Get(). If the data has not been
// populated, the reader will block pending the data. Once the data has been
// delivered, all readers will unblock and receive a reference to the Promise's
// data.
type Promise struct {
	signalC chan struct{} // Channel whose closing signals that the data is available.

	// onGet, if not nil, is invoked when Get is called.
	onGet func(context.Context)

	data any // The Promise's data.
	err  error       // The error status.
}

// New instantiates a new, empty Promise instance. The Promise's value will be
// the value returned by the supplied generator function.
//
// The generator will be invoked immediately in its own goroutine.
func New(ctx context.Context, gen Generator) *Promise {
	p := Promise{
		signalC: make(chan struct{}),
	}

	// Execute our generator function in a separate goroutine.
	go p.runGen(ctx, gen)
	return &p
}

// NewDeferred instantiates a new, empty Promise instance. The Promise's value
// will be the value returned by the supplied generator function.
//
// Unlike New, the generator function will not be immediately executed. Instead,
// it will be run when the first call to Get is made, and will use one of the
// Get callers' goroutines.
// goroutine a Get caller.
func NewDeferred(gen Generator) *Promise {
	var startOnce sync.Once

	p := Promise{
		signalC: make(chan struct{}),
	}
	p.onGet = func(ctx context.Context) {
		startOnce.Do(func() { p.runGen(ctx, gen) })
	}
	return &p
}

func (p *Promise) runGen(ctx context.Context, gen Generator) {
	defer close(p.signalC)
	p.data, p.err = gen(ctx)
}

// Get returns the promise's value. If the value isn't set, Get will block until
// the value is available, following the Context's timeout parameters.
//
// If the value is available, it will be returned with its error status. If the
// context times out or is cancelled, the appropriate context error will be
// returned.
func (p *Promise) Get(ctx context.Context) (any, error) {
	// If we have an onGet function, run it (deferred case).
	if p.onGet != nil {
		p.onGet(ctx)
	}

	// Block until at least one of these conditions is satisfied. If both are,
	// "select" will choose one pseudo-randomly.
	select {
	case <-p.signalC:
		return p.data, p.err

	case <-ctx.Done():
		// Make sure we don't actually have data.
		select {
		case <-p.signalC:
			return p.data, p.err

		default:
			return nil, ctx.Err()
		}
	}
}

// Peek returns the promise's current value. If the value isn't set, Peek will
// return immediately with ErrNoData.
func (p *Promise) Peek() (any, error) {
	select {
	case <-p.signalC:
		return p.data, p.err

	default:
		return nil, ErrNoData
	}
}
