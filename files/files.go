// Package files keeps a tool's own data in a directory several processes
// may share (two MCP sessions, `serve` and the CLI): lock files and atomic
// writes. See opinionated/keeping-data.md.
//
//   - Lock: hold NAME.lock while reading, changing and writing a file. It's a
//     file made with an exclusive create, so it works wherever files do. A
//     lock left by a process that died is broken once it's older than
//     Options.Stale, and a busy lock is reported with who holds it.
//   - WriteAtomic: readers see the old contents or the new, never part, so
//     reads need no lock.
package files

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Options for taking a lock.
type Options struct {
	// Wait is how long to wait for a lock someone else holds.
	Wait time.Duration
	// Retry is how often to look again.
	Retry time.Duration
	// Stale is how old a lock must be to count as left by a dead process.
	Stale time.Duration
}

// Defaults: wait 10 s, look every 20 ms, break locks older than 5 minutes.
var Defaults = Options{Wait: 10 * time.Second, Retry: 20 * time.Millisecond, Stale: 5 * time.Minute}

// Busy is the error when someone else held the lock for the whole wait.
type Busy struct {
	Holder string
	Held   time.Duration
}

func (b *Busy) Error() string {
	return fmt.Sprintf("locked by %s for %.1f s", b.Holder, b.Held.Seconds())
}

// Lock is a held lock; Release frees it.
type Lock struct {
	path, token string
}

func unique() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Take takes the lock at path (e.g. drafts/d3.lock), waiting up to
// o.Wait, or until ctx is done. holder describes the taker, for whoever
// finds it locked.
func Take(ctx context.Context, path, holder string, o Options) (*Lock, error) {
	token := unique()
	deadline := time.Now().Add(o.Wait)
	for {
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err == nil {
			_, werr := fmt.Fprintf(f, "%s\n%s\n", token, holder)
			cerr := f.Close()
			if err := errors.Join(werr, cerr); err != nil {
				_ = os.Remove(path)
				return nil, err
			}
			return &Lock{path: path, token: token}, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return nil, err
		}
		info, statErr := os.Stat(path)
		var age time.Duration
		if statErr == nil {
			age = time.Since(info.ModTime())
			if age > o.Stale {
				_ = os.Remove(path)
				continue
			}
		}
		if time.Now().After(deadline) {
			held, _ := os.ReadFile(path)
			who := "someone"
			if lines := strings.Split(string(held), "\n"); len(lines) > 1 && lines[1] != "" {
				who = lines[1]
			}
			return nil, &Busy{Holder: who, Held: age}
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(o.Retry):
		}
	}
}

// Release frees the lock, if it's still ours (it may have been broken as
// stale meanwhile).
func (l *Lock) Release() {
	if data, err := os.ReadFile(l.path); err == nil && strings.SplitN(string(data), "\n", 2)[0] == l.token {
		_ = os.Remove(l.path)
	}
}

// WriteAtomic writes data to path so readers see the old contents or the
// new, never part: a temporary file beside it, synced, then renamed over it.
func WriteAtomic(path string, data []byte) error {
	tmp := filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+"."+unique()+".tmp")
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	_, werr := f.Write(data)
	serr := f.Sync()
	cerr := f.Close()
	if err := errors.Join(werr, serr, cerr); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// ReadIfExists reads a file; a missing file is (nil, nil).
func ReadIfExists(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	return data, err
}

// StandardDir is where a tool keeps its data unless told otherwise:
// $<NAME>_DATA_DIR (e.g. ANKI_CARDS_DATA_DIR), else the platform's place for
// an app's data (~/Library/Application Support/NAME on macOS, $XDG_DATA_HOME/NAME or ~/.local/share/NAME on Linux,
// %APPDATA%\NAME on Windows). Not created here.
func StandardDir(name string) (string, error) {
	env := strings.ToUpper(strings.ReplaceAll(name, "-", "_")) + "_DATA_DIR"
	if d := os.Getenv(env); d != "" {
		return d, nil
	}
	if x := os.Getenv("XDG_DATA_HOME"); x != "" {
		return filepath.Join(x, name), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", name), nil
	case "windows":
		if a := os.Getenv("APPDATA"); a != "" {
			return filepath.Join(a, name), nil
		}
	}
	return filepath.Join(home, ".local", "share", name), nil
}
