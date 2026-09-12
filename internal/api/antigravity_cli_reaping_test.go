package api

import (
	"context"
	"errors"
	"os/exec"
	"runtime"
	"testing"
	"time"
)

func TestAgyFixtureProcessIsReaped(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	name := "cat"
	if runtime.GOOS == "windows" {
		name = "cmd.exe"
	}
	path, err := exec.LookPath(name)
	if err != nil {
		t.Fatal(err)
	}
	r := NewAntigravityCLIRunner(nil)
	defer r.Stop()
	for i := range 3 {
		sess, err := r.launch(path)
		if err != nil {
			t.Fatal(err)
		}
		if i == 2 {
			r.rootCancel()
			select {
			case <-sess.done:
			case <-time.After(5 * time.Second):
				t.Fatal("canceled process not reaped")
			}
		}
		r.killSession(sess)
		select {
		case <-sess.done:
		case <-time.After(time.Second):
			t.Fatal("started process was not reaped")
		}
	}
	if _, err := r.Fetch(context.Background()); !errors.Is(err, context.Canceled) {
		t.Fatalf("stopped runner launched again: %v", err)
	}
}
