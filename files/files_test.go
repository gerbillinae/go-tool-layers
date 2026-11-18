package files

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

var quick = Options{Wait: 60 * time.Millisecond, Retry: 10 * time.Millisecond, Stale: time.Minute}

func TestLockExcludesAndReleases(t *testing.T) {
	p := filepath.Join(t.TempDir(), "x.lock")
	ctx := context.Background()
	held, err := Take(ctx, p, "first", quick)
	if err != nil {
		t.Fatal(err)
	}
	var busy *Busy
	if _, err := Take(ctx, p, "second", quick); !errors.As(err, &busy) || busy.Holder != "first" {
		t.Fatalf("want busy by first, got %v", err)
	}
	held.Release()
	if _, err := os.Stat(p); err == nil {
		t.Fatal("released lock left behind")
	}
	if _, err := Take(ctx, p, "second", quick); err != nil {
		t.Fatal(err)
	}
}

func TestStaleLocksAreBroken(t *testing.T) {
	p := filepath.Join(t.TempDir(), "x.lock")
	_ = os.WriteFile(p, []byte("someone-elses\ndead process\n"), 0o644)
	old := time.Now().Add(-time.Hour)
	_ = os.Chtimes(p, old, old)
	l, err := Take(context.Background(), p, "me", quick)
	if err != nil {
		t.Fatal(err)
	}
	l.Release()
}

func TestABrokenLockIsntRemovedByItsOldHolder(t *testing.T) {
	p := filepath.Join(t.TempDir(), "x.lock")
	ctx := context.Background()
	old, _ := Take(ctx, p, "old", quick)
	_ = os.Remove(p) // broken as stale by someone
	newer, _ := Take(ctx, p, "new", quick)
	old.Release()
	if _, err := os.Stat(p); err != nil {
		t.Fatal("the old holder removed the new lock")
	}
	newer.Release()
}

func TestWriteAtomic(t *testing.T) {
	d := t.TempDir()
	p := filepath.Join(d, "a.json")
	_ = WriteAtomic(p, []byte("one"))
	_ = WriteAtomic(p, []byte("two"))
	if b, _ := os.ReadFile(p); string(b) != "two" {
		t.Fatal(string(b))
	}
	if es, _ := os.ReadDir(d); len(es) != 1 {
		t.Fatal("temporary files left")
	}
	if b, err := ReadIfExists(filepath.Join(d, "none")); b != nil || err != nil {
		t.Fatal(b, err)
	}
}

func TestStandardDir(t *testing.T) {
	t.Setenv("MY_TOOL_DATA_DIR", "/x")
	if d, _ := StandardDir("my-tool"); d != "/x" {
		t.Fatal(d)
	}
}
