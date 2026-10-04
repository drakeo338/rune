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

// Command runesvc is a stripped-down stand-in for the `rune` workspace
// server (see cmd/rune/main.go's --workspace-server / -x flag).
//
// It exists solely to give the SSH e2e tests a Linux binary they can
// install as `rune` inside the test container, exercising the full
// connectScheme path (whichCommand + workspaceExists + StartSchemeServer)
// without having to cross-compile the full rune binary (which depends on
// CGO and GUI libraries that don't cross-compile cleanly from a dev
// laptop).
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"gopkg.in/yaml.v3"
	"unstable.build/rune/internal/workspace"
	"unstable.build/rune/internal/workspace/workspacerpc"
	"unstable.build/rune/internal/workspace/workspacessh"
)

func main() {
	workspacePath := flag.String("x", "",
		"local workspace path to expose over the workspace gRPC server")
	dataDir := flag.String("datadir", "",
		"data directory used by the workspace server")
	install := flag.String("install", "",
		"CSV manifest of id@version packages to provision (mirrors the "+
			"real rune -x flag). When non-empty, runesvc loads "+
			"~/.rune/config.yaml and applies its gui.env block to this "+
			"process before serving, exercising the provisioning ordering.")
	flag.Parse()

	if *workspacePath == "" {
		fmt.Fprintln(os.Stderr, "runesvc: -x <workspace-path> is required")
		os.Exit(2)
	}

	// Mirror the real -x path's ordering: provisioning (install + config
	// load + gui.env apply) happens before the server starts serving, so a
	// child process spawned over the workspace RPC inherits the applied env.
	if *install != "" {
		emitProvisionProgress(*install)
		installPackageBins(*dataDir, *install)
		applyRemoteGUIEnv(*dataDir)
	}

	uri, err := workspaceapi.ParseURI("file://" + *workspacePath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "runesvc: parse uri:", err)
		os.Exit(3)
	}
	scheme, err := workspace.NewFileScheme(
		context.Background(), config.NopConfig(), uri)
	if err != nil {
		fmt.Fprintln(os.Stderr, "runesvc: new file scheme:", err)
		os.Exit(4)
	}
	defer scheme.Close()

	server := workspacerpc.NewServer(scheme,
		workspacerpc.CommandAuthorizerFunc(
			func(context.Context, workspaceapi.Cmd) error { return nil }))
	defer func() { _ = server.Stop() }()

	// Stand in for a slow pre-serving phase (e.g. a lengthy install). The
	// delay happens before StartSchemeServer emits the ServerReady sentinel,
	// so an e2e test can prove connectScheme waits for readiness and does not
	// hand back a client while stdout carries no gRPC server yet.
	sleepServeDelay(*dataDir)

	grpcServer := workspacessh.NewSchemeServer()
	if err := servePackages(grpcServer, *dataDir); err != nil {
		fmt.Fprintln(os.Stderr, "runesvc: serve packages:", err)
		os.Exit(6)
	}
	if err := workspacessh.StartSchemeServer(
		log.New(), server, grpcServer); err != nil {
		fmt.Fprintln(os.Stderr, "runesvc: start scheme server:", err)
		os.Exit(5)
	}
}

// sleepServeDelay blocks for the duration recorded in the remote data
// directory's serve_delay file, if present. It lets an e2e test inject a
// deterministic pre-serving delay without a test-only flag on the production
// connectScheme launch. A missing or malformed file is a no-op.
func sleepServeDelay(dataDir string) {
	if dataDir == "" {
		dataDir = defaultDataDir()
	}
	if dataDir == "" {
		return
	}
	data, err := os.ReadFile(filepath.Join(dataDir, "serve_delay"))
	if err != nil {
		return
	}
	d, err := time.ParseDuration(strings.TrimSpace(string(data)))
	if err != nil {
		fmt.Fprintf(os.Stderr, "runesvc: parse serve_delay: %v\n", err)
		return
	}
	fmt.Fprintf(os.Stderr, "runesvc: delaying serve by %s\n", d)
	time.Sleep(d)
}

// applyRemoteGUIEnv loads the remote data directory's config.yaml and applies
// its gui.env block to this process, standing in for the real -x server's
// post-install config load.
// Failures warn and continue, matching the never-abort provisioning policy.
func applyRemoteGUIEnv(dataDir string) {
	if dataDir == "" {
		dataDir = defaultDataDir()
	}
	if dataDir == "" {
		fmt.Fprintln(os.Stderr, "runesvc: cannot resolve data dir")
		return
	}
	configPath := filepath.Join(dataDir, "config.yaml")
	data, err := os.ReadFile(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "runesvc: read %s: %v\n", configPath, err)
		return
	}
	var doc struct {
		GUI struct {
			Env map[string]any `yaml:"env"`
		} `yaml:"gui"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		fmt.Fprintf(os.Stderr, "runesvc: parse %s: %v\n", configPath, err)
		return
	}
	for k, v := range doc.GUI.Env {
		if err := os.Setenv(k, fmt.Sprintf("%v", v)); err != nil {
			fmt.Fprintf(os.Stderr, "runesvc: setenv %s: %v\n", k, err)
		}
	}
}

func defaultDataDir() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		if u, uerr := user.Current(); uerr == nil {
			home = u.HomeDir
		}
	}
	if home == "" {
		return ""
	}
	return filepath.Join(home, ".rune")
}

// emitProvisionProgress streams JSON-Lines provisioning progress to stderr for
// each package in the manifest, standing in for the real -x server's install
// loop. The local side (workspacessh) forwards each line to a UI notification,
// so the e2e test can assert the ordered stream reaches the browser.
func emitProvisionProgress(manifest string) {
	entries := strings.Split(manifest, ",")
	total := len(entries)
	for i, entry := range entries {
		id, ver, _ := strings.Cut(entry, "@")
		index := i + 1
		emit(workspacessh.ProvisionProgress{
			Index: index, Total: total, Package: id, Version: ver,
			Phase: workspacessh.ProvisionPhaseInstalling,
		})
		emit(workspacessh.ProvisionProgress{
			Index: index, Total: total, Package: id, Version: ver,
			Phase: workspacessh.ProvisionPhaseActivating,
		})
	}
	emit(workspacessh.ProvisionProgress{
		Index: total, Total: total, Phase: workspacessh.ProvisionPhaseDone,
	})
	// Mirror the real -x path: after installs finish, a finalizing checkpoint
	// keeps the local progress bar alive through the config/env phase before
	// serving. The bar is closed by the local side at ServerReady.
	emit(workspacessh.ProvisionProgress{
		Phase: workspacessh.ProvisionPhaseFinalizing,
	})
}

func emit(p workspacessh.ProvisionProgress) {
	line, err := workspacessh.EncodeProvisionProgress(p)
	if err != nil {
		return
	}
	fmt.Fprint(os.Stderr, line)
}

// installPackageBins stands in for a real package install by writing an
// executable for each manifest entry into the remote data directory's bin
// directory and prepending it to PATH, exactly as the real -x server does via
// setupRuneBinPATH.
// Each fake tool is named after the package id and prints a recognizable line
// so an e2e test can run it through the workspace executor and prove the
// provisioned toolchain is on the served process's PATH.
func installPackageBins(dataDir, manifest string) {
	if dataDir == "" {
		dataDir = defaultDataDir()
	}
	if dataDir == "" {
		fmt.Fprintln(os.Stderr, "runesvc: cannot resolve data dir for bin install")
		return
	}
	binDir := filepath.Join(dataDir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "runesvc: mkdir %s: %v\n", binDir, err)
		return
	}
	for entry := range strings.SplitSeq(manifest, ",") {
		id, ver, _ := strings.Cut(entry, "@")
		if id == "" {
			continue
		}
		toolPath := filepath.Join(binDir, id)
		script := fmt.Sprintf("#!/bin/sh\necho \"%s ok %s\"\n", id, ver)
		if err := os.WriteFile(toolPath, []byte(script), 0o755); err != nil {
			fmt.Fprintf(os.Stderr, "runesvc: write %s: %v\n", toolPath, err)
			continue
		}
	}
	if err := os.Setenv("PATH", binDir+":"+os.Getenv("PATH")); err != nil {
		fmt.Fprintf(os.Stderr, "runesvc: set PATH: %v\n", err)
	}
}
