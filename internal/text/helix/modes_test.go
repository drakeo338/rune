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

package helix

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/clipboard"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/text"
)

// TestModeTransitions pins the normal / select / insert cycle and the
// status each mode reports through the public predicates.
func TestModeTransitions(t *testing.T) {
	t.Run("a fresh handler starts in normal mode", func(t *testing.T) {
		hx, _, _ := newHelix(t, "abc", term.Coordinates{})
		assert.True(t, hx.IsNormalMode())
		assert.False(t, hx.IsSelectMode())
		assert.False(t, hx.IsEditMode())
		assert.False(t, hx.IsSearchMode())
	})

	t.Run("v toggles select mode", func(t *testing.T) {
		hx, _, _ := newHelix(t, "abc", term.Coordinates{})
		send(t, hx, key('v'))
		assert.True(t, hx.IsSelectMode())
		assert.True(t, hx.IsNormalMode(), "select mode is normal mode with extend")
		send(t, hx, key('v'))
		assert.False(t, hx.IsSelectMode())
	})

	t.Run("esc leaves select mode", func(t *testing.T) {
		hx, _, _ := newHelix(t, "abc", term.Coordinates{})
		send(t, hx, key('v'))
		send(t, hx, namedKey(term.KeyEsc))
		assert.False(t, hx.IsSelectMode())
	})

	t.Run("esc leaves insert mode", func(t *testing.T) {
		hx, _, _ := newHelix(t, "abc", term.Coordinates{})
		send(t, hx, key('i'))
		require.True(t, hx.IsEditMode())
		send(t, hx, namedKey(term.KeyEsc))
		assert.True(t, hx.IsNormalMode())
		assert.False(t, hx.IsEditMode())
	})

	t.Run("an operator drops select mode", func(t *testing.T) {
		for _, ev := range []term.Event{
			key('d'), key('y'), key('~'), key('`'), key('>'),
			key('<'), key('p'), key('P'), key('R'),
			modKey(term.ModCtrl, 'c'),
		} {
			hx, _, _ := newHelix(t, "foo bar\nbaz", term.Coordinates{})
			send(t, hx, key('v'), key('w'))
			require.True(t, hx.IsSelectMode())
			send(t, hx, ev)
			assert.False(t, hx.IsSelectMode(), "%v must exit select mode", ev)
		}
	})

	// join_selections and format_selections are the operators Helix
	// deliberately leaves select mode alone for.
	t.Run("J and = keep select mode", func(t *testing.T) {
		for _, ev := range []term.Event{key('J'), key('=')} {
			hx, _, _ := newHelix(t, "foo bar\nbaz", term.Coordinates{})
			send(t, hx, key('v'), key('w'))
			require.True(t, hx.IsSelectMode())
			send(t, hx, ev)
			assert.True(t, hx.IsSelectMode(), "%v must stay in select mode", ev)
		}
	})

	t.Run("an increment that changes nothing keeps select mode", func(t *testing.T) {
		hx, _, _ := newHelix(t, "foo bar", term.Coordinates{})
		send(t, hx, key('v'), key('w'))
		require.True(t, hx.IsSelectMode())
		send(t, hx, modKey(term.ModCtrl, 'a'))
		assert.True(t, hx.IsSelectMode())

		hx2, _, _ := newHelix(t, "n 41", term.Coordinates{})
		send(t, hx2, key('v'), key('w'), key('e'))
		require.True(t, hx2.IsSelectMode())
		send(t, hx2, modKey(term.ModCtrl, 'a'))
		assert.False(t, hx2.IsSelectMode())
	})

	t.Run("SetNormalMode resets every pending state", func(t *testing.T) {
		hx, _, _ := newHelix(t, "abc", term.Coordinates{})
		send(t, hx, key('v'), key('"'))
		hx.SetNormalMode()
		assert.True(t, hx.IsNormalMode())
		assert.False(t, hx.IsSelectMode())
		// The abandoned register prefix must not swallow the next key.
		send(t, hx, key('l'))
		assert.Equal(t, term.Coordinates{X: 1}, hx.CursorAtScroll())
	})

	// Helix's minor modes are one shot unless the sticky Z variant is used.
	for _, tc := range []struct {
		name  string
		enter term.Event
		key   term.Event
	}{
		{name: "goto", enter: key('g'), key: key('h')},
		{name: "match", enter: key('m'), key: key('m')},
		{name: "view", enter: key('z'), key: key('z')},
		{name: "replace", enter: key('r'), key: key('x')},
		{name: "bracket forward", enter: key(']'), key: key('p')},
		{name: "bracket backward", enter: key('['), key: key('p')},
	} {
		t.Run(tc.name+" mode is one shot", func(t *testing.T) {
			hx, _, _ := newHelix(t, "one two\n\nthree", term.Coordinates{})
			send(t, hx, tc.enter)
			assert.False(t, hx.IsNormalMode(), "still in the minor mode")
			send(t, hx, tc.key)
			assert.True(t, hx.IsNormalMode())
		})

		t.Run(tc.name+" mode exits on esc", func(t *testing.T) {
			hx, _, _ := newHelix(t, "one two\n\nthree", term.Coordinates{})
			send(t, hx, tc.enter)
			send(t, hx, namedKey(term.KeyEsc))
			assert.True(t, hx.IsNormalMode())
		})

		t.Run(tc.name+" mode exits on an unbound key", func(t *testing.T) {
			hx, _, _ := newHelix(t, "one two\n\nthree", term.Coordinates{})
			send(t, hx, tc.enter)
			send(t, hx, key('Ω'))
			assert.True(t, hx.IsNormalMode())
		})
	}
}

// TestInsertModeKeys walks the insert-mode key table.
func TestInsertModeKeys(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content string
		at      term.Coordinates
		evs     []term.Event
		want    string
		wantAt  *term.Coordinates
	}{
		{name: "printable characters insert", content: "", evs: keys("iabc"),
			want: "abc"},
		{name: "space inserts a space", content: "ab",
			evs: []term.Event{key('i'), namedKey(term.KeySpace)}, want: " ab"},
		{name: "enter splits the line", content: "ab",
			at:  term.Coordinates{X: 1},
			evs: []term.Event{key('i'), namedKey(term.KeyEnter)}, want: "a\nb"},
		{name: "ctrl-j also splits the line", content: "ab",
			at:  term.Coordinates{X: 1},
			evs: []term.Event{key('i'), modKey(term.ModCtrl, 'j')}, want: "a\nb"},
		// Without an indent service TryIndent declines and the handler
		// falls back to a literal indent rune.
		{name: "tab inserts the indent rune", content: "ab",
			evs: []term.Event{key('i'), namedKey(term.KeyTab)}, want: " ab"},
		{name: "shift-tab inserts the indent rune", content: "ab",
			evs:  []term.Event{key('i'), modNamedKey(term.ModShift, term.KeyTab)},
			want: " ab"},
		{name: "backspace deletes the previous cell", content: "abc",
			at:  term.Coordinates{X: 2},
			evs: []term.Event{key('i'), namedKey(term.KeyBackspace)}, want: "ac"},
		{name: "shift-backspace deletes too", content: "abc",
			at:   term.Coordinates{X: 2},
			evs:  []term.Event{key('i'), modNamedKey(term.ModShift, term.KeyBackspace)},
			want: "ac"},
		{name: "ctrl-h deletes the previous cell", content: "abc",
			at:  term.Coordinates{X: 2},
			evs: []term.Event{key('i'), modKey(term.ModCtrl, 'h')}, want: "ac"},
		{name: "delete removes the cell under the caret", content: "abc",
			evs: []term.Event{key('i'), namedKey(term.KeyDelete)}, want: "bc"},
		{name: "ctrl-d removes the cell under the caret", content: "abc",
			evs: []term.Event{key('i'), modKey(term.ModCtrl, 'd')}, want: "bc"},
		{name: "backspace at the buffer start is inert", content: "abc",
			evs: []term.Event{key('i'), namedKey(term.KeyBackspace)}, want: "abc"},
		{name: "backspace joins lines", content: "ab\ncd",
			at:  term.Coordinates{Y: 1},
			evs: []term.Event{key('i'), namedKey(term.KeyBackspace)}, want: "abcd"},
		{name: "ctrl-w deletes the previous word", content: "foo bar",
			at:  term.Coordinates{X: 7},
			evs: []term.Event{key('a'), modKey(term.ModCtrl, 'w')}, want: "foo "},
		{name: "alt-backspace deletes the previous word", content: "foo bar",
			at:   term.Coordinates{X: 7},
			evs:  []term.Event{key('a'), modNamedKey(term.ModAlt, term.KeyBackspace)},
			want: "foo "},
		{name: "alt-d deletes the next word", content: "foo bar",
			evs:  []term.Event{key('i'), modKey(term.ModAlt, 'd')},
			want: " bar"},
		{name: "alt-delete deletes the next word", content: "foo bar",
			evs:  []term.Event{key('i'), modNamedKey(term.ModAlt, term.KeyDelete)},
			want: " bar"},
		{name: "ctrl-u kills back to the first non blank", content: "  foo",
			at:  term.Coordinates{X: 5},
			evs: []term.Event{key('i'), modKey(term.ModCtrl, 'u')}, want: "  "},
		{name: "ctrl-u at the first non blank kills the indent", content: "  foo",
			at:  term.Coordinates{X: 2},
			evs: []term.Event{key('i'), modKey(term.ModCtrl, 'u')}, want: "foo"},
		{name: "ctrl-u at column zero joins the previous line", content: "ab\ncd",
			at:  term.Coordinates{Y: 1},
			evs: []term.Event{key('i'), modKey(term.ModCtrl, 'u')}, want: "abcd"},
		{name: "ctrl-u at the buffer start is inert", content: "ab",
			evs: []term.Event{key('i'), modKey(term.ModCtrl, 'u')}, want: "ab"},
		{name: "ctrl-k kills to the line end", content: "abcd",
			at:  term.Coordinates{X: 2},
			evs: []term.Event{key('i'), modKey(term.ModCtrl, 'k')}, want: "ab"},
		{name: "ctrl-k at the line end joins", content: "ab\ncd",
			at:  term.Coordinates{X: 1},
			evs: []term.Event{key('a'), modKey(term.ModCtrl, 'k')}, want: "abcd"},
		{name: "insert accepts wide glyphs", content: "",
			evs: []term.Event{key('i'), key('世'), key('界')}, want: "世界"},
		{name: "backspace removes a whole wide glyph", content: "世界",
			at:  term.Coordinates{X: 1},
			evs: []term.Event{key('i'), namedKey(term.KeyBackspace)}, want: "界"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hx, buf, _ := newHelix(t, tc.content, tc.at)
			send(t, hx, tc.evs...)
			assert.Equal(t, tc.want, buf.String())
			if tc.wantAt != nil {
				assert.Equal(t, *tc.wantAt, hx.CursorAtScroll())
			}
		})
	}

	t.Run("tab snaps to the syntax indent level", func(t *testing.T) {
		hx, buf, _ := newHelix(t, "ab", term.Coordinates{})
		buf.WithView(testIndentView{View: buf.View(), indents: map[int]int{0: 0}})
		send(t, hx, key('i'), namedKey(term.KeyTab))
		assert.Equal(t, "  ab", buf.String())
	})

	t.Run("ctrl-c leaves insert mode", func(t *testing.T) {
		hx, buf, _ := newHelix(t, "ab", term.Coordinates{})
		send(t, hx, keys("iX")...)
		send(t, hx, modKey(term.ModCtrl, 'c'))
		assert.True(t, hx.IsNormalMode())
		assert.Equal(t, "Xab", buf.String())
	})

	t.Run("arrows move without leaving insert mode", func(t *testing.T) {
		for _, k := range []term.Key{
			term.KeyArrowLeft, term.KeyArrowRight,
			term.KeyArrowUp, term.KeyArrowDown,
			term.KeyHome, term.KeyEnd, term.KeyPgup, term.KeyPgdn,
		} {
			hx, _, _ := newHelix(t, "abc\ndef", term.Coordinates{X: 1, Y: 0})
			send(t, hx, key('i'))
			send(t, hx, namedKey(k))
			assert.True(t, hx.IsEditMode(), "%v must stay in insert mode", k)
		}
	})

	t.Run("ctrl-r inserts a register", func(t *testing.T) {
		hx, buf, clip := newHelix(t, "ab", term.Coordinates{})
		require.NoError(t, clip.Copy(registerNameToID('a'),
			clipboard.Data{Text: "REG", Metadata: text.StandardSelection}))
		send(t, hx, key('i'))
		send(t, hx, modKey(term.ModCtrl, 'r'), key('a'))
		assert.Equal(t, "REGab", buf.String())
	})

	t.Run("ctrl-r with an unknown register inserts nothing", func(t *testing.T) {
		hx, buf, _ := newHelix(t, "ab", term.Coordinates{})
		send(t, hx, key('i'))
		send(t, hx, modKey(term.ModCtrl, 'r'), modKey(term.ModCtrl, 'g'))
		assert.Equal(t, "ab", buf.String())
		assert.True(t, hx.IsEditMode())
	})

	t.Run("ctrl-r from an empty register inserts nothing", func(t *testing.T) {
		hx, buf, _ := newHelix(t, "ab", term.Coordinates{})
		send(t, hx, key('i'))
		send(t, hx, modKey(term.ModCtrl, 'r'), key('z'))
		assert.Equal(t, "ab", buf.String())
	})

	t.Run("ctrl-r swallows a zero key", func(t *testing.T) {
		hx, buf, _ := newHelix(t, "ab", term.Coordinates{})
		send(t, hx, key('i'))
		send(t, hx, modKey(term.ModCtrl, 'r'), term.Event{Type: term.EventKey})
		assert.Equal(t, "ab", buf.String())
		assert.True(t, hx.IsEditMode())
	})

	t.Run("ctrl-u on a blank line kills the whole indent", func(t *testing.T) {
		hx, buf, _ := newHelix(t, "   \nx", term.Coordinates{X: 3})
		send(t, hx, key('i'), modKey(term.ModCtrl, 'u'))
		assert.Equal(t, "\nx", buf.String())
	})

	// delete_char_backward dedents by a whole indent level when only
	// indentation precedes the caret.
	t.Run("backspace dedents", func(t *testing.T) {
		for _, tc := range []struct {
			name    string
			content string
			at      term.Coordinates
			want    string
		}{
			{name: "at an indent boundary", content: "    ab",
				at: term.Coordinates{X: 4}, want: "  ab"},
			{name: "off an indent boundary", content: "   ab",
				at: term.Coordinates{X: 3}, want: "  ab"},
			{name: "a single leading space", content: " ab",
				at: term.Coordinates{X: 1}, want: "ab"},
			{name: "after real text it deletes one cell", content: "  abc",
				at: term.Coordinates{X: 4}, want: "  ac"},
			{name: "a tab takes the plain path", content: "\t\tab",
				at: term.Coordinates{X: 2}, want: "\tab"},
		} {
			hx, buf, _ := newHelix(t, tc.content, tc.at)
			send(t, hx, key('i'), namedKey(term.KeyBackspace))
			assert.Equal(t, tc.want, buf.String(), tc.name)
		}
	})

	t.Run("ctrl-h dedents too", func(t *testing.T) {
		hx, buf, _ := newHelix(t, "    ab", term.Coordinates{X: 4})
		send(t, hx, key('i'), modKey(term.ModCtrl, 'h'))
		assert.Equal(t, "  ab", buf.String())
	})

	t.Run("the insert register records the session", func(t *testing.T) {
		hx, _, clip := newHelix(t, "", term.Coordinates{})
		send(t, hx, keys("iabc")...)
		send(t, hx, namedKey(term.KeyEsc))
		data, err := clip.Paste(registerNameToID('.'))
		require.NoError(t, err)
		assert.Equal(t, "abc", data.Text)
	})

	// i is the one entry that leaves a non-empty range behind with its
	// head at the start, so escaping keeps the caret where it was
	// rather than dropping it onto the text just typed.
	t.Run("esc after i keeps the caret on the original cell", func(t *testing.T) {
		hx, buf, _ := newHelix(t, "ab", term.Coordinates{})
		send(t, hx, keys("iX")...)
		send(t, hx, namedKey(term.KeyEsc))
		assert.Equal(t, "Xab", buf.String())
		assert.Equal(t, term.Coordinates{X: 1}, hx.CursorAtScroll())
		assert.Equal(t, "a", sel(t, hx), "normal mode always owns a selection")
	})

	t.Run("esc after a plain i does not move the caret", func(t *testing.T) {
		hx, _, _ := newHelix(t, "abcdef", term.Coordinates{X: 3})
		send(t, hx, key('i'), namedKey(term.KeyEsc))
		assert.Equal(t, term.Coordinates{X: 3}, hx.CursorAtScroll())
	})

	// Every other entry starts from an empty range, which the
	// min-width-1 invariant widens backwards on the way out.
	for _, tc := range []struct {
		name string
		evs  []term.Event
		want term.Coordinates
	}{
		{name: "a", evs: keys("aX"), want: term.Coordinates{X: 1}},
		{name: "A", evs: keys("AX"), want: term.Coordinates{X: 2}},
		{name: "I", evs: keys("IX"), want: term.Coordinates{}},
		{name: "o", evs: keys("oX"), want: term.Coordinates{Y: 1}},
		{name: "O", evs: keys("OX"), want: term.Coordinates{}},
		{name: "c", evs: keys("cX"), want: term.Coordinates{}},
	} {
		t.Run("esc after "+tc.name+" lands on the last insert", func(t *testing.T) {
			hx, _, _ := newHelix(t, "ab", term.Coordinates{})
			send(t, hx, tc.evs...)
			send(t, hx, namedKey(term.KeyEsc))
			assert.Equal(t, tc.want, hx.CursorAtScroll())
			assert.Equal(t, "X", sel(t, hx))
		})
	}
}

// TestBracketedPaste pins the paste burst protocol.
func TestBracketedPaste(t *testing.T) {
	burst := func(str string) []term.Event {
		evs := []term.Event{{Type: term.EventPasteStart}}
		evs = append(evs, keys(str)...)
		return append(evs, term.Event{Type: term.EventPasteEnd})
	}

	t.Run("a burst in insert mode is inserted verbatim", func(t *testing.T) {
		hx, buf, _ := newHelix(t, "", term.Coordinates{})
		send(t, hx, key('i'))
		send(t, hx, burst("hello")...)
		assert.Equal(t, "hello", buf.String())
	})

	t.Run("a burst in normal mode is swallowed", func(t *testing.T) {
		hx, buf, _ := newHelix(t, "ab", term.Coordinates{})
		send(t, hx, burst("hello")...)
		assert.Equal(t, "ab", buf.String())
		assert.True(t, hx.IsNormalMode())
	})

	t.Run("an empty burst is a no-op", func(t *testing.T) {
		hx, buf, _ := newHelix(t, "ab", term.Coordinates{})
		send(t, hx, key('i'))
		send(t, hx, term.Event{Type: term.EventPasteStart},
			term.Event{Type: term.EventPasteEnd})
		assert.Equal(t, "ab", buf.String())
	})

	t.Run("burst keys never reach the keymap", func(t *testing.T) {
		hx, buf, _ := newHelix(t, "abc", term.Coordinates{})
		send(t, hx, term.Event{Type: term.EventPasteStart})
		send(t, hx, keys("dd")...)
		send(t, hx, term.Event{Type: term.EventPasteEnd})
		assert.Equal(t, "abc", buf.String())
	})
}

// TestUnboundMultiSelectionKeys pins the phase-one contract: Helix's
// selection-set commands stay unbound so they cannot be mistaken for
// working, and the keymap layer can claim them later.
func TestUnboundMultiSelectionKeys(t *testing.T) {
	for _, ev := range []term.Event{
		key('s'), key('S'), key('C'), key('&'), key('('), key(')'),
		key(','), key('|'), key('!'), key('$'), key('K'), key(':'),
		modKey(term.ModAlt, 'C'), modKey(term.ModAlt, 's'),
		modKey(term.ModAlt, '_'), modKey(term.ModAlt, '-'),
		modKey(term.ModAlt, '('), modKey(term.ModAlt, ')'),
		modKey(term.ModAlt, ','), modKey(term.ModAlt, 'I'),
		modKey(term.ModAlt, 'a'),
		modKey(term.ModAlt, 'K'), modKey(term.ModAlt, '|'),
		modKey(term.ModAlt, '!'), modKey(term.ModCtrl, 'z'),
	} {
		hx, buf, _ := newHelix(t, "foo bar", term.Coordinates{})
		_, handled := hx.Handle(ev)
		assert.False(t, handled, "%v must stay unbound", ev)
		assert.Equal(t, "foo bar", buf.String())
		assert.True(t, hx.IsNormalMode())
	}
}

// TestUnboundSyntaxKeys covers the sibling and parent-node motions,
// which need a syntax tree this handler does not own. They stay free
// for the keymap layer instead of being approximated.
func TestUnboundSyntaxKeys(t *testing.T) {
	for _, ev := range []term.Event{
		modKey(term.ModAlt, 'n'), modKey(term.ModAlt, 'p'),
		modKey(term.ModAlt, 'b'), modKey(term.ModAlt, 'e'),
		modNamedKey(term.ModAlt, term.KeyArrowLeft),
		modNamedKey(term.ModAlt, term.KeyArrowRight),
	} {
		hx, buf, _ := newHelix(t, "f(a b)", term.Coordinates{})
		_, handled := hx.Handle(ev)
		assert.False(t, handled, "%v must stay unbound", ev)
		assert.Equal(t, "f(a b)", buf.String())
		assert.True(t, hx.IsNormalMode())
	}
}

// TestUnboundLayerKeys covers the prefixes Helix reserves for the
// window manager, the pickers and the shell, which belong to the IDE
// rather than the buffer.
func TestUnboundLayerKeys(t *testing.T) {
	for _, ev := range []term.Event{
		modKey(term.ModCtrl, 'w'), namedKey(term.KeySpace),
	} {
		hx, buf, _ := newHelix(t, "foo bar", term.Coordinates{})
		_, handled := hx.Handle(ev)
		assert.False(t, handled, "%v must stay unbound", ev)
		assert.Equal(t, "foo bar", buf.String())
		assert.True(t, hx.IsNormalMode())
	}
}

// TestUnboundBracketKeys pins the [ and ] entries that need
// diagnostics, VCS or a syntax tree. Only paragraphs and add_newline
// are served here.
func TestUnboundBracketKeys(t *testing.T) {
	for _, prefix := range []rune{'[', ']'} {
		for _, ch := range []rune{'d', 'D', 'g', 'G', 'f', 't', 'a', 'c', 'e', 'T', 'x'} {
			hx, _, _ := newHelix(t, "foo bar", term.Coordinates{})
			send(t, hx, key(prefix))
			_, handled := hx.Handle(key(ch))
			assert.False(t, handled, "%c%c must stay unbound", prefix, ch)
			assert.True(t, hx.IsNormalMode(), "%c%c still leaves bracket mode", prefix, ch)
		}
	}
}

// TestUnboundNavigationKeys pins the goto-mode entries Helix reserves
// for the language server and buffer list.
func TestUnboundNavigationKeys(t *testing.T) {
	for _, ch := range []rune{'d', 'D', 'y', 'r', 'i', 'a', 'm', 'n', 'p', 'f'} {
		hx, _, _ := newHelix(t, "foo bar", term.Coordinates{})
		send(t, hx, key('g'))
		_, handled := hx.Handle(key(ch))
		assert.False(t, handled, "g%c must stay unbound", ch)
		assert.True(t, hx.IsNormalMode(), "g%c still leaves goto mode", ch)
	}
}

// TestMacroBindings pins Q and q against a recorder/player double.
func TestMacroBindings(t *testing.T) {
	t.Run("Q toggles recording", func(t *testing.T) {
		rec := new(fakeMacroRecorder)
		hx, _, _ := newHelix(t, "abc", term.Coordinates{}, WithMacroRecorder(rec))
		send(t, hx, key('Q'))
		assert.Equal(t, 1, rec.starts)
		rec.recording = true
		send(t, hx, key('Q'))
		assert.Equal(t, 1, rec.stops)
	})

	t.Run("Q records into the selected register", func(t *testing.T) {
		rec := new(fakeMacroRecorder)
		hx, _, _ := newHelix(t, "abc", term.Coordinates{}, WithMacroRecorder(rec))
		send(t, hx, keys("\"aQ")...)
		assert.Equal(t, registerNameToID('a'), rec.lastID)
	})

	t.Run("q replays a register", func(t *testing.T) {
		player := new(fakeMacroPlayer)
		hx, _, _ := newHelix(t, "abc", term.Coordinates{}, WithMacroPlayer(player))
		send(t, hx, keys("3q")...)
		assert.Equal(t, 3, player.count)
	})

	t.Run("without a recorder Q is unhandled", func(t *testing.T) {
		hx, _, _ := newHelix(t, "abc", term.Coordinates{})
		_, handled := hx.Handle(key('Q'))
		assert.False(t, handled)
	})
}

type fakeMacroRecorder struct {
	recording bool
	starts    int
	stops     int
	lastID    string
}

func (r *fakeMacroRecorder) IsRecording() bool { return r.recording }

func (r *fakeMacroRecorder) Start(id string) {
	r.starts++
	r.lastID = id
}

func (r *fakeMacroRecorder) Stop() { r.stops++ }

type fakeMacroPlayer struct {
	playing bool
	id      string
	count   int
}

func (p *fakeMacroPlayer) IsPlaying() bool { return p.playing }

func (p *fakeMacroPlayer) Play(id string, count int) error {
	p.id = id
	p.count = count
	return nil
}

// TestUndoRedo pins u/U and the Alt variants, which the wrapper claims
// before the state machine sees them.
func TestUndoRedo(t *testing.T) {
	t.Run("u undoes the last edit", func(t *testing.T) {
		hx, buf, _ := newHelix(t, "abc", term.Coordinates{})
		send(t, hx, key('d'))
		require.Equal(t, "bc", buf.String())
		send(t, hx, key('u'))
		assert.Equal(t, "abc", buf.String())
	})

	t.Run("U redoes", func(t *testing.T) {
		hx, buf, _ := newHelix(t, "abc", term.Coordinates{})
		send(t, hx, keys("du")...)
		require.Equal(t, "abc", buf.String())
		send(t, hx, key('U'))
		assert.Equal(t, "bc", buf.String())
	})

	t.Run("a counted undo walks back several edits", func(t *testing.T) {
		hx, buf, _ := newHelix(t, "abcd", term.Coordinates{})
		send(t, hx, keys("ddd")...)
		require.Equal(t, "d", buf.String())
		send(t, hx, keys("3u")...)
		assert.Equal(t, "abcd", buf.String())
	})

	t.Run("alt-u is undo with a count", func(t *testing.T) {
		hx, buf, _ := newHelix(t, "abcd", term.Coordinates{})
		send(t, hx, keys("ddd")...)
		send(t, hx, key('2'))
		send(t, hx, modKey(term.ModAlt, 'u'))
		assert.Equal(t, "bcd", buf.String())
	})

	t.Run("undo on a pristine buffer is inert", func(t *testing.T) {
		hx, buf, _ := newHelix(t, "abc", term.Coordinates{})
		send(t, hx, key('u'))
		assert.Equal(t, "abc", buf.String())
	})

	t.Run("an insert session undoes as one edit", func(t *testing.T) {
		hx, buf, _ := newHelix(t, "", term.Coordinates{})
		send(t, hx, keys("ihello")...)
		send(t, hx, namedKey(term.KeyEsc))
		require.Equal(t, "hello", buf.String())
		send(t, hx, key('u'))
		assert.Equal(t, "", buf.String())
	})

	t.Run("ctrl-s splits an insert session into two undo steps", func(t *testing.T) {
		hx, buf, _ := newHelix(t, "", term.Coordinates{})
		send(t, hx, keys("iab")...)
		send(t, hx, modKey(term.ModCtrl, 's'))
		send(t, hx, keys("cd")...)
		send(t, hx, namedKey(term.KeyEsc))
		require.Equal(t, "abcd", buf.String())
		send(t, hx, key('u'))
		assert.Equal(t, "ab", buf.String())
	})

	t.Run("undo does not write to the clipboard", func(t *testing.T) {
		hx, _, clip := newHelix(t, "abc", term.Coordinates{})
		send(t, hx, key('d'))
		seedClipboard(t, clip, "KEEP", text.StandardSelection)
		send(t, hx, key('u'))
		data, err := clip.Paste(clipboard.DefaultRegisterID)
		require.NoError(t, err)
		assert.Equal(t, "KEEP", data.Text)
	})
}

// TestDotRepeat pins `.`, which replays the last insert session.
func TestDotRepeat(t *testing.T) {
	t.Run("repeats the last insert", func(t *testing.T) {
		hx, buf, _ := newHelix(t, "", term.Coordinates{})
		send(t, hx, keys("ihi")...)
		send(t, hx, namedKey(term.KeyEsc))
		require.Equal(t, "hi", buf.String())
		send(t, hx, key('.'))
		assert.Equal(t, "hhii", buf.String())
	})

	t.Run("with nothing recorded it is unhandled", func(t *testing.T) {
		hx, _, _ := newHelix(t, "abc", term.Coordinates{})
		_, handled := hx.Handle(key('.'))
		assert.False(t, handled)
	})

	t.Run("a repeat stays in normal mode", func(t *testing.T) {
		hx, _, _ := newHelix(t, "", term.Coordinates{})
		send(t, hx, keys("ix")...)
		send(t, hx, namedKey(term.KeyEsc))
		send(t, hx, key('.'))
		assert.True(t, hx.IsNormalMode())
	})
}

// TestSearchMode pins / and ? plus the n/N repeats.
func TestSearchMode(t *testing.T) {
	t.Run("/ opens the search prompt", func(t *testing.T) {
		hx, _, _ := newHelix(t, "abc", term.Coordinates{})
		send(t, hx, key('/'))
		assert.True(t, hx.IsSearchMode())
		assert.False(t, hx.IsNormalMode())
	})

	t.Run("esc closes the prompt", func(t *testing.T) {
		hx, _, _ := newHelix(t, "abc", term.Coordinates{})
		send(t, hx, key('/'))
		send(t, hx, namedKey(term.KeyEsc))
		assert.True(t, hx.IsNormalMode())
		assert.False(t, hx.IsSearchMode())
	})

	t.Run("a search selects the match", func(t *testing.T) {
		hx, _, _ := newHelix(t, "foo bar foo", term.Coordinates{})
		send(t, hx, key('/'))
		send(t, hx, keys("bar")...)
		send(t, hx, namedKey(term.KeyEnter))
		assert.Equal(t, "bar", sel(t, hx))
		assert.True(t, hx.IsNormalMode())
	})

	t.Run("n walks forward through matches", func(t *testing.T) {
		hx, _, _ := newHelix(t, "ab ab ab", term.Coordinates{})
		send(t, hx, key('/'))
		send(t, hx, keys("ab")...)
		send(t, hx, namedKey(term.KeyEnter))
		first := hx.CursorAtScroll()
		send(t, hx, key('n'))
		second := hx.CursorAtScroll()
		assert.NotEqual(t, first, second)
		assert.Equal(t, "ab", sel(t, hx))
		send(t, hx, key('N'))
		assert.Equal(t, first, hx.CursorAtScroll())
	})

	t.Run("Search only arms the pattern", func(t *testing.T) {
		hx, _, _ := newHelix(t, "foo bar foo", term.Coordinates{})
		hx.Search("bar")
		assert.Equal(t, "f", sel(t, hx), "the caret does not move")
		send(t, hx, key('n'))
		assert.Equal(t, "bar", sel(t, hx))
	})

	t.Run("search can be disabled", func(t *testing.T) {
		hx, _, _ := newHelix(t, "abc", term.Coordinates{}, WithSearch(false))
		_, handled := hx.Handle(key('/'))
		assert.False(t, handled)
		assert.True(t, hx.IsNormalMode())
	})

	t.Run("* searches for the selection", func(t *testing.T) {
		hx, _, _ := newHelix(t, "foo bar foo", term.Coordinates{})
		send(t, hx, keys("w*")...)
		assert.Contains(t, strings.TrimSpace(sel(t, hx)), "foo")
	})
}
