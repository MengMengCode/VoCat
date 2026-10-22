package modem

import (
	"errors"
	"path/filepath"
	"strings"
	"sync"
)

var ErrATPortBusy = errors.New("modem: AT port is busy")

var atPortLeases = struct {
	sync.Mutex
	paths map[string]struct{}
}{paths: make(map[string]struct{})}

func tryAcquireATPort(path string) (release func(), ok bool) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, false
	}

	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	if absolute, err := filepath.Abs(path); err == nil {
		path = absolute
	}
	path = filepath.Clean(path)

	atPortLeases.Lock()
	if _, exists := atPortLeases.paths[path]; exists {
		atPortLeases.Unlock()
		return nil, false
	}
	atPortLeases.paths[path] = struct{}{}
	atPortLeases.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			atPortLeases.Lock()
			delete(atPortLeases.paths, path)
			atPortLeases.Unlock()
		})
	}, true
}
