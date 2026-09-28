package clip

import (
	"bytes"
	"sync"
	"testing"
	"time"
)

type fake struct {
	value   []byte
	changed chan struct{}
	writes  int
}

func (f *fake) Write(b []byte) (<-chan struct{}, error) {
	f.value = bytes.Clone(b)
	f.writes++
	f.changed = make(chan struct{})
	return f.changed, nil
}
func (f *fake) Read() ([]byte, error) { return bytes.Clone(f.value), nil }
func TestExpiryAndOwnership(t *testing.T) {
	for _, mode := range []string{"expire", "changed", "same-new-generation"} {
		t.Run(mode, func(t *testing.T) {
			f := &fake{}
			var mu sync.Mutex
			generation := ""
			lock := func() (func(), error) { mu.Lock(); return mu.Unlock, nil }
			current := func(v string) (bool, error) {
				if len(v) > 4 && v[:4] == "set:" {
					generation = v[4:]
					return true, nil
				}
				return generation == v, nil
			}
			err := Serve(f, Request{Text: []byte("secret"), Duration: time.Millisecond}, func() error {
				if mode == "changed" {
					f.value = []byte("new")
					close(f.changed)
				}
				if mode == "same-new-generation" {
					generation = "new"
				}
				return nil
			}, lock, current)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "expire" && len(f.value) != 0 {
				t.Fatal("did not clear")
			}
			if mode != "expire" && f.writes != 1 {
				t.Fatal("overwrote newer clipboard")
			}
		})
	}
}
