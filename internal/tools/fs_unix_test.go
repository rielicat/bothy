//go:build unix

package tools

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// swapForFIFO replaces a workspace path with a FIFO nobody writes to, as box
// code sharing the workspace can between a tool's Plan and its Exec.
func swapForFIFO(t *testing.T, ws, rel string) {
	t.Helper()
	p := filepath.Join(ws, filepath.FromSlash(rel))
	if err := os.RemoveAll(p); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(p, 0o660); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		// Releases an open(2) still blocked on the FIFO, so a failing test
		// does not leave a goroutine stuck in the kernel.
		if f, err := os.OpenFile(p, os.O_RDWR|syscall.O_NONBLOCK, 0); err == nil {
			_ = f.Close()
		}
	})
}

// returnsPromptly runs fn and fails the test when it is still blocked after
// two seconds; fn must fail (a FIFO is not a regular file).
func returnsPromptly(t *testing.T, what string, fn func() error) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- fn() }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatalf("%s on a FIFO succeeded", what)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("%s blocked on a FIFO swapped in after Plan", what)
	}
}

func TestFSToolsDoNotBlockOnAFIFO(t *testing.T) {
	t.Run("fs_read exec", func(t *testing.T) {
		env, ws := workspace(t)
		a, err := plan(t, fsRead{}, env, `{"path":"top.txt"}`)
		if err != nil {
			t.Fatal(err)
		}
		swapForFIFO(t, ws, "top.txt")
		returnsPromptly(t, "fs_read", func() error {
			_, err := (fsRead{}).Exec(context.Background(), env, a)
			return err
		})
	})
	t.Run("fs_write append exec", func(t *testing.T) {
		env, ws := workspace(t)
		a, err := plan(t, fsWrite{}, env, `{"path":"top.txt","content":"more\n","mode":"append"}`)
		if err != nil {
			t.Fatal(err)
		}
		swapForFIFO(t, ws, "top.txt")
		returnsPromptly(t, "fs_write append", func() error {
			_, err := (fsWrite{}).Exec(context.Background(), env, a)
			return err
		})
	})
	t.Run("fs_write plan", func(t *testing.T) {
		env, ws := workspace(t)
		swapForFIFO(t, ws, "top.txt")
		returnsPromptly(t, "fs_write plan", func() error {
			_, err := plan(t, fsWrite{}, env, `{"path":"top.txt","content":"x\n"}`)
			return err
		})
	})
	t.Run("fs_list exec", func(t *testing.T) {
		env, ws := workspace(t)
		for _, recursive := range []string{"false", "true"} {
			a, err := plan(t, fsList{}, env, `{"path":"notes","recursive":`+recursive+`}`)
			if err != nil {
				t.Fatal(err)
			}
			swapForFIFO(t, ws, "notes")
			returnsPromptly(t, "fs_list recursive="+recursive, func() error {
				_, err := (fsList{}).Exec(context.Background(), env, a)
				return err
			})
			if err := os.Remove(filepath.Join(ws, "notes")); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Join(ws, "notes", "deep"), 0o755); err != nil {
				t.Fatal(err)
			}
		}
	})
	t.Run("fs_list skips a FIFO entry", func(t *testing.T) {
		env, ws := workspace(t)
		swapForFIFO(t, ws, "notes/deep")
		res, err := run(t, fsList{}, env, `{"path":".","recursive":true}`)
		if err != nil {
			t.Fatal(err)
		}
		if res.Content == "" {
			t.Fatal("empty listing")
		}
	})
	t.Run("fs_delete plan", func(t *testing.T) {
		env, ws := workspace(t)
		swapForFIFO(t, ws, "top.txt")
		a, err := plan(t, fsDelete{}, env, `{"path":"top.txt"}`)
		if err != nil || a.Diff != "" {
			t.Fatalf("plan of a FIFO = %+v %v", a, err)
		}
	})
}

func TestReadRegularRefusesSpecialFiles(t *testing.T) {
	_, ws := workspace(t)
	root, err := os.OpenRoot(ws)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	swapForFIFO(t, ws, "top.txt")
	returnsPromptly(t, "readExisting", func() error {
		_, err := readExisting(root, "top.txt")
		return err
	})
	if _, err := readExisting(root, "notes"); err == nil {
		t.Fatal("readExisting of a folder succeeded")
	}
	cur, err := readExisting(root, "notes/a.txt")
	if err != nil || !cur.exists || string(cur.data) != "alpha\nbeta\ngamma\n" {
		t.Fatalf("readExisting = %+v %v", cur, err)
	}
	if cur, err := readExisting(root, "missing.txt"); err != nil || cur.exists || cur.data == nil {
		t.Fatalf("readExisting of a missing file = %+v %v", cur, err)
	}
}
