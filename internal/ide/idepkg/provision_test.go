// Copyright (C) 2017-2026 The Rune Authors
// SPDX-License-Identifier: GPL-3.0-or-later
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License as published by
// the Free Software Foundation, either version 3 of the License, or (at
// your option) any later version.
//
// This program is distributed in the hope that it will be useful, but
// WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the GNU
// General Public License for more details.
//
// You should have received a copy of the GNU General Public License
// along with this program. If not, see <https://www.gnu.org/licenses/>.

package idepkg

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/blue/iterator"
	"github.com/unstablebuild/blue/release"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi/storagestub"
	"unstable.build/rune/internal/ide/idepkg/idepkgtest"
)

type fakeProvisionSource struct {
	installed []string
	inUse     map[string]string
}

func (f fakeProvisionSource) ListInstalledPackages(
	context.Context,
) (iterator.Iterator[string], error) {
	return iterator.FromSlice(f.installed), nil
}

func (f fakeProvisionSource) PackageVersionInUse(
	_ context.Context, pkgID string,
) (release.Version, bool, error) {
	v, ok := f.inUse[pkgID]
	return release.Version(v), ok, nil
}

func TestBuildProvisionManifest(t *testing.T) {
	src := fakeProvisionSource{
		installed: []string{"rune-python", "rune-go", "rune-rust"},
		inUse: map[string]string{
			"rune-go":     "1.2.3",
			"rune-python": "4.5.6",
			// rune-rust installed but not in use -> skipped
		},
	}
	entries, err := BuildProvisionManifest(context.Background(), src)
	require.NoError(t, err)
	require.Equal(t, []ProvisionEntry{
		{ID: "rune-go", Version: "1.2.3"},
		{ID: "rune-python", Version: "4.5.6"},
	}, entries)
}

func TestEncodeParseProvisionManifestRoundTrip(t *testing.T) {
	entries := []ProvisionEntry{
		{ID: "rune-go", Version: "1.2.3"},
		{ID: "rune-python", Version: "4.5.6"},
	}
	encoded := EncodeProvisionManifest(entries)
	require.Equal(t, "rune-go@1.2.3,rune-python@4.5.6", encoded)

	decoded := ParseProvisionManifest(encoded)
	require.Equal(t, entries, decoded)
}

func TestEncodeProvisionManifestEmpty(t *testing.T) {
	require.Equal(t, "", EncodeProvisionManifest(nil))
	require.Equal(t, "", EncodeProvisionManifest([]ProvisionEntry{}))
}

func TestParseProvisionManifestEmpty(t *testing.T) {
	require.Nil(t, ParseProvisionManifest(""))
	require.Nil(t, ParseProvisionManifest("   "))
}

func TestEncodeProvisionManifestRejectsShellMetacharacters(t *testing.T) {
	cases := []ProvisionEntry{
		{ID: "evil;rm -rf", Version: "1.0"},
		{ID: "pkg", Version: "1.0 && curl evil"},
		{ID: "pkg`whoami`", Version: "1.0"},
		{ID: "pkg$USER", Version: "1.0"},
		{ID: "pkg'quote", Version: "1.0"},
		{ID: `pkg"quote`, Version: "1.0"},
		{ID: "pkg with space", Version: "1.0"},
	}
	for _, bad := range cases {
		good := ProvisionEntry{ID: "rune-go", Version: "1.2.3"}
		encoded := EncodeProvisionManifest([]ProvisionEntry{good, bad})
		require.Equal(t, "rune-go@1.2.3", encoded,
			"unsafe entry %q@%q must be dropped", bad.ID, bad.Version)
	}
}

func TestParseProvisionManifestRejectsUnsafe(t *testing.T) {
	require.Nil(t, ParseProvisionManifest("pkg@1.0;rm -rf /"))
	require.Nil(t, ParseProvisionManifest("pkg@$(whoami)"))
	require.Nil(t, ParseProvisionManifest("pkg@1.0 && curl evil"))
}

func TestParseProvisionManifestSkipsMalformed(t *testing.T) {
	entries := ParseProvisionManifest("rune-go@1.2.3,noversion,@1.0,rune-rust@2.0")
	require.Equal(t, []ProvisionEntry{
		{ID: "rune-go", Version: "1.2.3"},
		{ID: "rune-rust", Version: "2.0"},
	}, entries)
}

func TestPackageVersionInUseReadsLibSymlink(t *testing.T) {
	dataDir := t.TempDir()
	m := &Manager{dataDir: dataDir}

	_, ok := versionInUse(t, m, "rune-go")
	require.False(t, ok, "no symlink yet")

	require.NoError(t, os.MkdirAll(
		makePackageVersionDirname(dataDir, "rune-go", "1.2.3"), 0o777))
	require.NoError(t, os.MkdirAll(filepath.Dir(
		makePackageLibDirname(dataDir, "rune-go")), 0o777))
	require.NoError(t, os.Symlink(
		makePackageVersionDirname(dataDir, "rune-go", "1.2.3"),
		makePackageLibDirname(dataDir, "rune-go")))

	version, ok := versionInUse(t, m, "rune-go")
	require.True(t, ok)
	require.Equal(t, release.Version("1.2.3"), version)
}

// TestProvisioningManagerSharesEditorPartition is the guard against the remote
// `rune -x` provisioning path drifting from the editor's package-manager
// storage partition. Both must read/write StoragePartition, so a package the
// editor recorded (written here directly into WithPartition(root,
// StoragePartition)) must be visible to a Manager built by
// NewProvisioningManager from the same root. A partition rename that misses
// either call site breaks this test rather than silently splitting state.
func TestProvisioningManagerSharesEditorPartition(t *testing.T) {
	root := storagestub.NewInMemoryService()

	// Editor side: the pkgManager stores into WithPartition(root,
	// StoragePartition). Reproduce a completed install directly.
	editorPart := storageapi.WithPartition(root, StoragePartition)
	val := newPkgVersionValue("rune-go", "1.2.3")
	val.Complete = true
	require.NoError(t, editorPart.Create(
		context.Background(), idepkgTestStorageKey("rune-go", "1.2.3"), val))

	// Provisioning side: build from the SAME root; it must partition
	// identically and therefore see the editor's entry.
	mgr, storage := NewProvisioningManager(
		root, nil, newLocalScheme(t.TempDir()),
		t.TempDir(), filepath.Join(t.TempDir(), "config.yaml"), "", nil,
		idepkgtest.TrustStore())
	t.Cleanup(func() { _ = storage.Close() })

	it, err := mgr.ListInstalledPackages(context.Background())
	require.NoError(t, err)
	installed, err := iterator.ToSlice(context.Background(), it)
	require.NoError(t, err)
	require.Contains(t, installed, "rune-go",
		"provisioning manager must share the editor's %q partition", StoragePartition)
}
