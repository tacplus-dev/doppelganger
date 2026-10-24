// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	pb "github.com/tacplus-dev/doppelganger/tacplus/types/services/doppelganger"
)

// ContextStore is the context this instance answers from, on disk under root:
//
//	tenant/         the tenant's shared context (UpdateTenantContext)
//	users/<user>/   one user's own context (UpdateContext), user = hex(principal)
//
// Each is replaced whole and atomically. It lives on disk until stratum is
// ported, when it moves there.
type ContextStore struct {
	root string
}

func NewContextStore(root string) *ContextStore { return &ContextStore{root: root} }

func (c *ContextStore) TenantDir() string { return filepath.Join(c.root, "tenant") }

// UserDir names a user's context by the hex of their principal: a principal
// is an opaque string and may carry anything, a path segment may not.
func (c *ContextStore) UserDir(principal string) string {
	return filepath.Join(c.root, "users", hex.EncodeToString([]byte(principal)))
}

var errBadContextPath = errors.New("context file path must be relative, clean and inside the context")

// Replace makes files the whole content of dir: written beside it, then
// swapped in, so a reader sees the old context or the new one, never a mix.
func (c *ContextStore) Replace(dir string, files []*pb.ContextFile) (int, int64, error) {
	for _, f := range files {
		if !validContextPath(f.GetPath()) {
			return 0, 0, fmt.Errorf("%w: %q", errBadContextPath, f.GetPath())
		}
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
		return 0, 0, err
	}
	suffix := make([]byte, 4)
	if _, err := rand.Read(suffix); err != nil {
		return 0, 0, err
	}
	fresh := dir + ".new." + hex.EncodeToString(suffix)
	var bytes int64
	for _, f := range files {
		p := filepath.Join(fresh, filepath.FromSlash(f.GetPath()))
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			os.RemoveAll(fresh)
			return 0, 0, err
		}
		if err := os.WriteFile(p, f.GetContent(), 0o600); err != nil {
			os.RemoveAll(fresh)
			return 0, 0, err
		}
		bytes += int64(len(f.GetContent()))
	}
	if len(files) == 0 {
		if err := os.MkdirAll(fresh, 0o700); err != nil {
			return 0, 0, err
		}
	}
	old := dir + ".old." + hex.EncodeToString(suffix)
	if err := os.Rename(dir, old); err != nil && !errors.Is(err, os.ErrNotExist) {
		os.RemoveAll(fresh)
		return 0, 0, err
	}
	if err := os.Rename(fresh, dir); err != nil {
		os.RemoveAll(fresh)
		return 0, 0, err
	}
	os.RemoveAll(old)
	return len(files), bytes, nil
}

// validContextPath admits a clean, relative, slash-separated path with no
// parent references, so a file can land only inside the context directory.
func validContextPath(p string) bool {
	if p == "" || strings.HasPrefix(p, "/") || strings.Contains(p, "\\") {
		return false
	}
	clean := filepath.ToSlash(filepath.Clean(p))
	if clean != p || clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return false
	}
	return true
}

// Load concatenates the tenant's context and the user's own, each file tagged
// with its scope and path. A missing directory is an empty context; no files
// in either is an error, because an answer from nothing is not an answer.
func (c *ContextStore) Load(user string) (string, error) {
	var b strings.Builder
	n := 0
	for _, scope := range []struct{ name, dir string }{{"tenant", c.TenantDir()}, {"user", c.UserDir(user)}} {
		text, count, err := loadRepo(scope.dir)
		if err != nil {
			return "", err
		}
		n += count
		if count > 0 {
			fmt.Fprintf(&b, "## %s context\n\n%s", scope.name, text)
		}
	}
	if n == 0 {
		return "", errors.New("no context for this caller: the tenant has none and the user has uploaded none")
	}
	return b.String(), nil
}
