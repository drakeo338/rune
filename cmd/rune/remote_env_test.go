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

package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"unstable.build/rune/internal/workspace"
)

// setFlagForTest temporarily points a *string flag at value and restores it.
func setFlagForTest(t *testing.T, target *string, value string) {
	t.Helper()
	old := *target
	*target = value
	t.Cleanup(func() { *target = old })
}

func newTestFileScheme(t *testing.T, root string) (workspace.Workspace, workspaceapi.URI) {
	t.Helper()
	uri, err := workspaceapi.CurrentUserHostURI(root)
	require.NoError(t, err)
	scheme, err := workspace.NewFileScheme(context.Background(), config.NopConfig(), uri)
	require.NoError(t, err)
	t.Cleanup(func() { _ = scheme.Close() })
	cwd := workspace.NewSchemeWorkspace(uri, scheme, func(fn func()) bool { fn(); return true })
	return cwd, uri
}

// TestLoadRemoteConfigAppliesGUIEnv asserts that once a package install
// has merged a gui.env block into the remote ~/.rune config, loading that
// config applies the vars to the process environment so the tools the -x
// server spawns inherit them.
func TestLoadRemoteConfigAppliesGUIEnv(t *testing.T) {
	const (
		envKey = "RUNE_TEST_REMOTE_GOROOT"
		envVal = "/remote/go/root"
	)
	require.NoError(t, os.Unsetenv(envKey))
	t.Cleanup(func() { _ = os.Unsetenv(envKey) })

	dataDir := t.TempDir()
	configPath := filepath.Join(dataDir, "config.yaml")
	// The remote config as it exists AFTER a package install merged its
	// gui.env block.
	merged := "editor:\n  mode: modal\ngui:\n  env:\n    " + envKey + ": " + envVal + "\n"
	require.NoError(t, os.WriteFile(configPath, []byte(merged), 0o644))

	setFlagForTest(t, flagConfigPath, configPath)
	setFlagForTest(t, flagDataPath, dataDir)

	cwd, uri := newTestFileScheme(t, t.TempDir())
	loadRemoteConfigAndApplyEnv(cwd, uri)

	assert.Equal(t, envVal, os.Getenv(envKey),
		"gui.env from the remote config must be applied to the process env")

	// ~/.rune/bin must be on PATH so package executables resolve.
	assert.Contains(t, os.Getenv("PATH"), filepath.Join(dataDir, "bin"),
		"~/.rune/bin must be prepended to PATH")
}

// TestWorkspaceOverlayWinsOverHomeConfig asserts the workspace-root
// .rune/config.yaml overlay is layered on top of the remote-home config.
func TestWorkspaceOverlayWinsOverHomeConfig(t *testing.T) {
	const envKey = "RUNE_TEST_OVERLAY_VAR"
	require.NoError(t, os.Unsetenv(envKey))
	t.Cleanup(func() { _ = os.Unsetenv(envKey) })

	dataDir := t.TempDir()
	configPath := filepath.Join(dataDir, "config.yaml")
	require.NoError(t, os.WriteFile(configPath,
		[]byte("editor:\n  mode: modal\ngui:\n  env:\n    "+envKey+": home\n"), 0o644))

	setFlagForTest(t, flagConfigPath, configPath)
	setFlagForTest(t, flagDataPath, dataDir)

	wsRoot := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(wsRoot, ".rune"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(wsRoot, workspaceConfigFilename),
		[]byte("gui:\n  env:\n    "+envKey+": workspace\n"), 0o644))

	cwd, uri := newTestFileScheme(t, wsRoot)
	loadRemoteConfigAndApplyEnv(cwd, uri)

	assert.Equal(t, "workspace", os.Getenv(envKey),
		"workspace-root .rune/config.yaml must override the home config")
}

// TestResolveRemoteEditorMode asserts a host's package manager resolves
// the user's editor mode from its ~/.rune config so RUNE_EDITOR_MODE is
// predeclared for package config.star scripts. exo resolves to its fallback,
// a missing config falls back to the vim default rather than an empty
// (undefined) mode, and a config that still says "modal" reaches packages as
// "vim".
func TestResolveRemoteEditorMode(t *testing.T) {
	t.Run("resolves exo to configured fallback", func(t *testing.T) {
		dataDir := t.TempDir()
		configPath := filepath.Join(dataDir, "config.yaml")
		require.NoError(t, os.WriteFile(configPath, []byte(
			"editor:\n  mode: exo\n  exo:\n    command: vim {file}\n"+
				"    goto: \"<esc>:{line}<enter>\"\n    quit: \"<esc>:qa<enter>\"\n"+
				"    fallback: helix\n"), 0o644))
		setFlagForTest(t, flagConfigPath, configPath)

		assert.Equal(t, "helix", resolveRemoteEditorMode())
	})

	t.Run("missing config defaults to vim", func(t *testing.T) {
		setFlagForTest(t, flagConfigPath, filepath.Join(t.TempDir(), "config.yaml"))

		assert.Equal(t, "vim", resolveRemoteEditorMode())
	})

	t.Run("resolves the deprecated modal mode to vim", func(t *testing.T) {
		dataDir := t.TempDir()
		configPath := filepath.Join(dataDir, "config.yaml")
		require.NoError(t, os.WriteFile(configPath,
			[]byte("editor:\n  mode: modal\n"), 0o644))
		setFlagForTest(t, flagConfigPath, configPath)

		assert.Equal(t, "vim", resolveRemoteEditorMode())
	})

	t.Run("resolves standard mode", func(t *testing.T) {
		dataDir := t.TempDir()
		configPath := filepath.Join(dataDir, "config.yaml")
		require.NoError(t, os.WriteFile(configPath,
			[]byte("editor:\n  mode: standard\n"), 0o644))
		setFlagForTest(t, flagConfigPath, configPath)

		assert.Equal(t, "standard", resolveRemoteEditorMode())
	})
}
