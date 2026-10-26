/*
Copyright 2025 Gluesys FlexA Inc.

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

package driver

import (
	"sync"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// volumeLocks serializes operations on one volume. A second request for a
// volume that is already being worked on gets ABORTED, as the CSI spec asks,
// and the sidecar or kubelet retries it later.
type volumeLocks struct {
	mu   sync.Mutex
	busy map[string]struct{}
}

func newVolumeLocks() *volumeLocks {
	return &volumeLocks{busy: map[string]struct{}{}}
}

// acquire returns a release func, or an ABORTED status if key is busy.
func (l *volumeLocks) acquire(key string) (func(), error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, ok := l.busy[key]; ok {
		return nil, status.Errorf(codes.Aborted, "an operation on volume %s is already in progress", key)
	}
	l.busy[key] = struct{}{}
	return func() {
		l.mu.Lock()
		delete(l.busy, key)
		l.mu.Unlock()
	}, nil
}
