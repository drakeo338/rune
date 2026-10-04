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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/blue/iterator"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"unstable.build/rune/internal/ide/idepkg"
)

type goPackages struct {
	idepkg.PackageManager
}

func (goPackages) LibDir(context.Context, string) (iterator.Iterator[string], error) {
	return iterator.FromSlice([]string{"/pkg/go/bin/go"}), nil
}

func TestServedPackagesWaitForTheEditor(t *testing.T) {
	ctx := context.Background()
	var s servedPackages

	_, err := s.LibDir(ctx, "go")
	assert.Equal(t, codes.Unavailable, status.Code(err),
		"peers retry once the editor that owns the packages is up")

	s.set(goPackages{})
	it, err := s.LibDir(ctx, "go")
	require.NoError(t, err)
	paths, err := iterator.ToSlice(ctx, it)
	require.NoError(t, err)
	assert.Equal(t, []string{"/pkg/go/bin/go"}, paths)
}
