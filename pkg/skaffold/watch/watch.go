/*
Copyright 2018 The Skaffold Authors

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package watch

import (
	"context"
	"time"

	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
)

// Factory creates Watcher instances.
type Factory func() Watcher

type ChangeFn func(WatchEvents) error

// Watcher monitors files changes for multiples components.
type Watcher interface {
	Register(deps func() ([]string, error), onChange ChangeFn) error
	Run(ctx context.Context, pollInterval time.Duration, onChange ChangeFn) error
}

type watchList []*component

// NewWatcher creates a new Watcher.
func NewWatcher() Watcher {
	return &watchList{}
}

type component struct {
	deps     func() ([]string, error)
	onChange ChangeFn
	state    fileMap
}

// Register adds a new component to the watch list.
func (w *watchList) Register(deps func() ([]string, error), onChange ChangeFn) error {
	state, err := stat(deps)
	if err != nil {
		return errors.Wrap(err, "listing files")
	}

	*w = append(*w, &component{
		deps:     deps,
		onChange: onChange,
		state:    state,
	})
	return nil
}

// Run watches files until the context is cancelled or an error occurs.
func (w *watchList) Run(ctx context.Context, pollInterval time.Duration, onChange ChangeFn) error {
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			changed := 0
			var e WatchEvents
			for _, component := range *w {
				state, err := stat(component.deps)
				if err != nil {
					return errors.Wrap(err, "listing files")
				}
				e = events(component.state, state)

				if e.hasChanged() {
					if err := component.onChange(e); err != nil {
						logrus.Warnf("on change error: %s")
					}
					component.state = state
					changed++
				}
			}

			if changed > 0 {
				if err := onChange(e); err != nil {
					return errors.Wrap(err, "calling final callback")
				}
			}
		}
	}
}
