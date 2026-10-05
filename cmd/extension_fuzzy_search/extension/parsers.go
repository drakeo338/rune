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

package extension

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/sirupsen/logrus"
	sitter "github.com/tree-sitter/go-tree-sitter"
	"github.com/unstablebuild/blue/iterator"
	"unstable.build/rune/internal/extension"
	"unstable.build/rune/internal/ide/syntax/treesitter"
)

type parser struct {
	closed bool
	parser *sitter.Parser
	lang   *sitter.Language
	query  *sitter.Query
	lib    uintptr
}

func newParser(
	ctx context.Context, langID string,
	pkg *extension.PkgManager,
	queryFile, query string,
) (ret *parser, err error) {
	it, err := pkg.LibDir(ctx, langID)
	if err != nil {
		err = fmt.Errorf("scan language package installation: %w", err)
		return
	}
	files, err := iterator.ToSlice(ctx, it)
	_ = it.Close()
	if err != nil {
		err = fmt.Errorf("list files: %w", err)
		return
	}
	var langfile string
	logrus.Tracef("found files: %v", files)
	for _, path := range files {
		switch filepath.Base(path) {
		case treesitter.ParserFilename:
			langfile = path
		case queryFile:
			// override path with absolute path
			queryFile = path
		}
	}

	if langfile == "" || (queryFile == "" && query == "") {
		err = errors.New("could not find parser file or " +
			"definitions in language installation")
		return
	}

	ret = new(parser)
	ret.lib, err = purego.Dlopen(langfile, purego.RTLD_NOW|purego.RTLD_GLOBAL)
	if err != nil {
		err = fmt.Errorf("dlopen %q: %w", langfile, err)
		return
	}

	parserID := fmt.Sprintf("tree_sitter_%s", langID)

	var lang func() uintptr
	sym, err := purego.Dlsym(ret.lib, parserID)
	if err != nil {
		_ = purego.Dlclose(ret.lib)
		err = fmt.Errorf("load symbol %q: %w", parserID, err)
		return
	}
	purego.RegisterFunc(&lang, sym)

	language := sitter.NewLanguage(unsafe.Pointer(lang()))
	parser := sitter.NewParser()
	err = parser.SetLanguage(language)
	if err != nil {
		_ = purego.Dlclose(ret.lib)
		parser.Close()
		err = fmt.Errorf("set parser language: %v", err)
		return
	}
	ret.lang = language
	ret.parser = parser

	if query != "" {
		var qerr *sitter.QueryError
		ret.query, qerr = sitter.NewQuery(ret.lang, query)
		if qerr != nil {
			err = qerr
		}
	} else {
		err = ret.initQueryFile(language, queryFile)
	}
	if err != nil {
		_ = purego.Dlclose(ret.lib)
		parser.Close()
		err = errInvalidQuery
		return
	}
	return
}

func (p *parser) initQueryFile(
	language *sitter.Language, queryFile string,
) error {
	data, err := os.ReadFile(queryFile)
	if err != nil {
		return fmt.Errorf("read locals file: %v", err)
	}
	locals, qerr := sitter.NewQuery(language, string(data))
	if qerr != nil {
		return fmt.Errorf("compile query: %v", qerr)
	}
	p.query = locals
	return nil
}

func (t *parser) Close() (ret error) {
	if t.closed {
		return nil
	}
	t.closed = true
	t.parser.Close()
	t.query.Close()
	ret = purego.Dlclose(t.lib)
	return
}
