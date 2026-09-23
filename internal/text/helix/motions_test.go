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
	"github.com/unstablebuild/rune-go-sdk/term"
)

// motionCase drives one keystroke sequence against a buffer and checks
// where the caret and the selection ended up.
type motionCase struct {
	name    string
	content string
	at      term.Coordinates
	evs     []term.Event
	wantAt  term.Coordinates
	wantSel string
	// skipSel leaves the selection unchecked for cases where only the
	// caret position is meaningful.
	skipSel bool
}

func runMotionCases(t *testing.T, cases []motionCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hx, _, _ := newHelix(t, tc.content, tc.at)
			send(t, hx, tc.evs...)
			assert.Equal(t, tc.wantAt, hx.CursorAtScroll(), "caret position")
			if !tc.skipSel {
				assert.Equal(t, tc.wantSel, sel(t, hx), "selection")
			}
		})
	}
}

// TestSelectionInvariant pins Helix's core rule: a range is never empty.
// Selection::ensure_invariants widens a collapsed range to one grapheme,
// so every operator always has something to act on.
func TestSelectionInvariant(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content string
		at      term.Coordinates
		want    string
	}{
		{name: "start of buffer", content: "hello", want: "h"},
		{name: "mid line", content: "hello", at: term.Coordinates{X: 2}, want: "l"},
		{name: "last cell", content: "hello", at: term.Coordinates{X: 4}, want: "o"},
		{name: "second line", content: "ab\ncd", at: term.Coordinates{Y: 1}, want: "c"},
		{name: "wide glyph", content: "世界", want: "世"},
		{name: "tab cell", content: "\tx", want: "\t"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hx, _, _ := newHelix(t, tc.content, tc.at)
			assert.Equal(t, tc.want, sel(t, hx))
		})
	}
}

// TestEmptyBufferIsSafe pins that a zero-length buffer does not panic
// and leaves every command a no-op.
func TestEmptyBufferIsSafe(t *testing.T) {
	for _, ev := range []term.Event{
		key('w'), key('b'), key('e'), key('h'), key('j'), key('k'), key('l'),
		key('d'), key('y'), key('p'), key('x'), key('X'), key('%'), key('~'),
		key('J'), key(';'),
		modKey(term.ModAlt, 'x'), modKey(term.ModAlt, ';'),
		modKey(term.ModCtrl, 'a'), modKey(term.ModCtrl, 'x'),
	} {
		hx, buf, _ := newHelix(t, "", term.Coordinates{})
		require.NotPanics(t, func() { hx.Handle(ev) }, "event %v", ev)
		assert.Equal(t, "", buf.String())
	}

	// The indent operators do write, so they only get the crash check.
	for _, ev := range []term.Event{key('>'), key('<'), key('=')} {
		hx, _, _ := newHelix(t, "", term.Coordinates{})
		require.NotPanics(t, func() { hx.Handle(ev) }, "event %v", ev)
	}
}

// TestCaretMotions pins h/j/k/l and the arrow keys: put_cursor with
// Movement::Move collapses the range onto the landing cell.
func TestCaretMotions(t *testing.T) {
	runMotionCases(t, []motionCase{
		{name: "l moves right", content: "hello", evs: keys("l"),
			wantAt: term.Coordinates{X: 1}, wantSel: "e"},
		{name: "h moves left", content: "hello", at: term.Coordinates{X: 2},
			evs: keys("h"), wantAt: term.Coordinates{X: 1}, wantSel: "e"},
		{name: "j moves down", content: "ab\ncd", evs: keys("j"),
			wantAt: term.Coordinates{Y: 1}, wantSel: "c"},
		{name: "k moves up", content: "ab\ncd", at: term.Coordinates{Y: 1},
			evs: keys("k"), wantAt: term.Coordinates{}, wantSel: "a"},
		{name: "arrow right", content: "hello", evs: []term.Event{namedKey(term.KeyArrowRight)},
			wantAt: term.Coordinates{X: 1}, wantSel: "e"},
		{name: "arrow left", content: "hello", at: term.Coordinates{X: 1},
			evs: []term.Event{namedKey(term.KeyArrowLeft)}, wantAt: term.Coordinates{}, wantSel: "h"},
		{name: "arrow down", content: "ab\ncd", evs: []term.Event{namedKey(term.KeyArrowDown)},
			wantAt: term.Coordinates{Y: 1}, wantSel: "c"},
		{name: "arrow up", content: "ab\ncd", at: term.Coordinates{Y: 1},
			evs: []term.Event{namedKey(term.KeyArrowUp)}, wantAt: term.Coordinates{}, wantSel: "a"},
		{name: "h clamps at line start", content: "hello", evs: keys("hhh"),
			wantAt: term.Coordinates{}, wantSel: "h"},
		{name: "l clamps at line end", content: "ab", evs: keys("lllll"),
			wantAt: term.Coordinates{X: 1}, wantSel: "b"},
		{name: "k clamps at first line", content: "ab\ncd", evs: keys("kkk"),
			wantAt: term.Coordinates{}, wantSel: "a"},
		{name: "j clamps at last line", content: "ab\ncd", evs: keys("jjj"),
			wantAt: term.Coordinates{Y: 1}, wantSel: "c"},
		{name: "count repeats l", content: "abcdef", evs: keys("3l"),
			wantAt: term.Coordinates{X: 3}, wantSel: "d"},
		{name: "count repeats j", content: "a\nb\nc\nd", evs: keys("3j"),
			wantAt: term.Coordinates{Y: 3}, wantSel: "d"},
		{name: "multi digit count", content: strings.Repeat("x", 30), evs: keys("12l"),
			wantAt: term.Coordinates{X: 12}, wantSel: "x"},
		{name: "count larger than buffer clamps", content: "ab\ncd", evs: keys("99j"),
			wantAt: term.Coordinates{Y: 1}, wantSel: "c"},
		{name: "j onto a shorter line clamps the column", content: "abcd\nx\nabcd",
			at: term.Coordinates{X: 3}, evs: keys("j"), wantAt: term.Coordinates{Y: 1}, wantSel: "x"},
		{name: "j past a shorter line restores the column", content: "abcd\nx\nabcd",
			at: term.Coordinates{X: 3}, evs: keys("jj"),
			wantAt: term.Coordinates{X: 3, Y: 2}, wantSel: "d"},
		{name: "j across an empty line", content: "ab\n\ncd", evs: keys("j"),
			wantAt: term.Coordinates{Y: 1}, wantSel: ""},
	})
}

// TestWordMotions pins the word grammar from helix-core word_move: the
// motion selects what it travels over, w stops before the word it
// found, and repeats advance rather than standing still.
func TestWordMotions(t *testing.T) {
	runMotionCases(t, []motionCase{
		{name: "w selects up to the next word", content: "foo bar baz", evs: keys("w"),
			wantAt: term.Coordinates{X: 3}, wantSel: "foo "},
		{name: "w advances on repeat", content: "foo bar baz", evs: keys("ww"),
			wantAt: term.Coordinates{X: 7}, wantSel: "bar "},
		{name: "w from mid word", content: "foo bar", at: term.Coordinates{X: 1},
			evs: keys("w"), wantAt: term.Coordinates{X: 3}, wantSel: "oo "},
		{name: "w from the last cell of a word", content: "foo bar",
			at: term.Coordinates{X: 2}, evs: keys("w"),
			wantAt: term.Coordinates{X: 3}, wantSel: "o "},
		{name: "w stops at a punctuation boundary", content: "a.b c", evs: keys("w"),
			wantAt: term.Coordinates{X: 1}, wantSel: "."},
		{name: "W treats punctuation as word material", content: "a.b c", evs: keys("W"),
			wantAt: term.Coordinates{X: 3}, wantSel: "a.b "},
		{name: "e selects through the word end", content: "foo bar", evs: keys("e"),
			wantAt: term.Coordinates{X: 2}, wantSel: "foo"},
		{name: "e advances on repeat", content: "foo bar", evs: keys("ee"),
			wantAt: term.Coordinates{X: 6}, wantSel: " bar"},
		{name: "E spans punctuation", content: "a.b c", evs: keys("E"),
			wantAt: term.Coordinates{X: 2}, wantSel: "a.b"},
		{name: "b selects backwards", content: "foo bar", at: term.Coordinates{X: 6},
			evs: keys("b"), wantAt: term.Coordinates{X: 4}, wantSel: "bar"},
		{name: "b advances backwards on repeat", content: "foo bar",
			at: term.Coordinates{X: 6}, evs: keys("bb"),
			wantAt: term.Coordinates{}, wantSel: "foo "},
		{name: "b after w reverses over the same text", content: "foo bar",
			evs: keys("wb"), wantAt: term.Coordinates{}, wantSel: "foo "},
		{name: "B spans punctuation", content: "a.b c", at: term.Coordinates{X: 4},
			evs: keys("B"), wantAt: term.Coordinates{}, wantSel: "a.b "},
		{name: "w at the buffer end stays put", content: "ab", at: term.Coordinates{X: 1},
			evs: keys("w"), wantAt: term.Coordinates{X: 1}, wantSel: "b"},
		{name: "b at the buffer start stays put", content: "ab", evs: keys("b"),
			wantAt: term.Coordinates{}, wantSel: "a"},
		{name: "w wraps to the next line", content: "ab\ncd", at: term.Coordinates{X: 1},
			evs: keys("w"), wantAt: term.Coordinates{X: 1, Y: 1}, wantSel: "cd"},
		{name: "2w covers only the last leg", content: "foo bar baz", evs: keys("2w"),
			wantAt: term.Coordinates{X: 7}, wantSel: "bar "},
		{name: "2e covers only the last leg", content: "foo bar baz", evs: keys("2e"),
			wantAt: term.Coordinates{X: 6}, wantSel: " bar"},
		{name: "w over leading whitespace", content: "  foo", evs: keys("w"),
			wantAt: term.Coordinates{X: 1}, wantSel: "  "},
		{name: "e over wide glyphs", content: "世界 x", evs: keys("e"),
			wantAt: term.Coordinates{X: 1}, wantSel: "世界"},
		{name: "w stops before a tab run", content: "a\t b", evs: keys("w"),
			wantAt: term.Coordinates{X: 2}, wantSel: "a\t "},
		{name: "e re-anchors past a word it already ends on", content: "a\t b",
			evs: keys("e"), wantAt: term.Coordinates{X: 3}, wantSel: "\t b"},
		{name: "w skips blank lines and re-anchors on the word",
			content: "foo\n\nbar", at: term.Coordinates{X: 2}, evs: keys("w"),
			wantAt: term.Coordinates{X: 2, Y: 2}, wantSel: "bar"},
		{name: "b skips blank lines backwards", content: "foo\n\nbar",
			at: term.Coordinates{X: 2, Y: 2}, evs: keys("b"),
			wantAt: term.Coordinates{Y: 2}, wantSel: "bar"},
		{name: "e on the last cell of the buffer stays put", content: "foo bar",
			at: term.Coordinates{X: 6}, evs: keys("e"),
			wantAt: term.Coordinates{X: 6}, wantSel: "r"},
		{name: "b from a word start crosses to the previous word",
			content: "foo bar", at: term.Coordinates{X: 4}, evs: keys("b"),
			wantAt: term.Coordinates{}, wantSel: "foo "},
		{name: "w runs into trailing whitespace", content: "foo   ",
			evs: keys("w"), wantAt: term.Coordinates{X: 5}, wantSel: "foo   "},
		{name: "w stops on a punctuation run", content: "a,,,b", evs: keys("w"),
			wantAt: term.Coordinates{X: 3}, wantSel: ",,,"},
		{name: "e ends on a punctuation run", content: "a,,,b", evs: keys("e"),
			wantAt: term.Coordinates{X: 3}, wantSel: ",,,"},
		{name: "b stops on a punctuation run", content: "a,,,b",
			at: term.Coordinates{X: 4}, evs: keys("b"),
			wantAt: term.Coordinates{X: 1}, wantSel: ",,,"},
		{name: "B swallows a punctuation run", content: "a,,,b",
			at: term.Coordinates{X: 4}, evs: keys("B"),
			wantAt: term.Coordinates{}, wantSel: "a,,,b"},
		{name: "3w covers only the last leg", content: "foo bar baz",
			evs: keys("3w"), wantAt: term.Coordinates{X: 10}, wantSel: "baz"},
		{name: "an overshooting count clamps at the buffer start",
			content: "foo bar baz", at: term.Coordinates{X: 10}, evs: keys("5b"),
			wantAt: term.Coordinates{}, wantSel: "foo "},
		{name: "w over wide glyphs", content: "x 世界 y", evs: keys("ww"),
			wantAt: term.Coordinates{X: 4}, wantSel: "世界 "},
		{name: "b over wide glyphs", content: "x 世界 y",
			at: term.Coordinates{X: 5}, evs: keys("b"),
			wantAt: term.Coordinates{X: 2}, wantSel: "世界 "},
		{name: "w on a one cell buffer stays put", content: "a", evs: keys("w"),
			wantAt: term.Coordinates{}, wantSel: "a"},
		{name: "w stops at the line ending of a blank line", content: "  \n  x",
			evs: keys("w"), wantAt: term.Coordinates{X: 1}, wantSel: "  "},
		{name: "e crosses a line ending", content: "foo\nbar",
			at: term.Coordinates{X: 2}, evs: keys("e"),
			wantAt: term.Coordinates{X: 2, Y: 1}, wantSel: "bar"},
		{name: "b crosses a line ending", content: "foo\nbar",
			at: term.Coordinates{Y: 1}, evs: keys("b"),
			wantAt: term.Coordinates{}, wantSel: "foo"},
		// A blank line has no cells, so the caret sits on the line
		// ending itself. word_move still has to walk off it.
		{name: "e from a blank line reaches the next word end",
			content: "## Examples\n\n        <.flash />",
			at: term.Coordinates{Y: 1}, evs: keys("e"),
			wantAt: term.Coordinates{X: 9, Y: 2}, wantSel: "        <."},
		{name: "w from a blank line reaches the next word start",
			content: "## Examples\n\n        <.flash />",
			at: term.Coordinates{Y: 1}, evs: keys("w"),
			wantAt: term.Coordinates{X: 7, Y: 2}, wantSel: "        "},
		{name: "e from a blank line between words", content: "a\n\nbb cc",
			at: term.Coordinates{Y: 1}, evs: keys("e"),
			wantAt: term.Coordinates{X: 1, Y: 2}, wantSel: "bb"},
		{name: "b from a blank line reaches the previous word start",
			content: "aa bb\n\ncc", at: term.Coordinates{Y: 1}, evs: keys("b"),
			wantAt: term.Coordinates{X: 3, Y: 0}, wantSel: "bb"},
	})
}

// TestFindCharMotions pins f/t/F/T. Helix offsets the search start for
// till motions so a repeat makes progress.
func TestFindCharMotions(t *testing.T) {
	runMotionCases(t, []motionCase{
		{name: "f selects through the match", content: "foo,bar",
			evs: append(keys("f"), key(',')), wantAt: term.Coordinates{X: 3}, wantSel: "foo,"},
		{name: "t stops before the match", content: "foo,bar",
			evs: append(keys("t"), key(',')), wantAt: term.Coordinates{X: 2}, wantSel: "foo"},
		{name: "F selects backwards through the match", content: "foo,bar",
			at: term.Coordinates{X: 6}, evs: append(keys("F"), key(',')),
			wantAt: term.Coordinates{X: 3}, wantSel: ",bar"},
		{name: "T stops after the match", content: "foo,bar",
			at: term.Coordinates{X: 6}, evs: append(keys("T"), key(',')),
			wantAt: term.Coordinates{X: 4}, wantSel: "bar"},
		{name: "t skips the adjacent match", content: "a,b,c",
			evs: append(keys("t"), key(',')), wantAt: term.Coordinates{X: 2}, wantSel: "a,b"},
		{name: "t with no further match keeps the range", content: "a,b,c",
			evs:    append(append(keys("t"), key(',')), append(keys("t"), key(','))...),
			wantAt: term.Coordinates{X: 2}, wantSel: "a,b"},
		{name: "f with a count finds the nth match", content: "a,b,c,d",
			evs: append(keys("2f"), key(',')), wantAt: term.Coordinates{X: 3}, wantSel: "a,b,"},
		{name: "f with no match leaves the selection alone", content: "abc",
			evs: append(keys("f"), key('z')), wantAt: term.Coordinates{}, wantSel: "a"},
		{name: "f on <space> uses the space key", content: "ab cd",
			evs:    []term.Event{key('f'), namedKey(term.KeySpace)},
			wantAt: term.Coordinates{X: 2}, wantSel: "ab "},
		{name: "f on <tab> uses a literal tab", content: "ab\tcd",
			evs:    []term.Event{key('f'), namedKey(term.KeyTab)},
			wantAt: term.Coordinates{X: 2}, wantSel: "ab\t"},
		{name: "esc cancels a pending find", content: "foo,bar",
			evs:    []term.Event{key('f'), namedKey(term.KeyEsc)},
			wantAt: term.Coordinates{}, wantSel: "f"},
	})
}

// TestRepeatLastMotion pins A-. replaying the last f/t/F/T.
func TestRepeatLastMotion(t *testing.T) {
	hx, _, _ := newHelix(t, "a,b,c,d", term.Coordinates{})
	send(t, hx, key('f'), key(','))
	require.Equal(t, term.Coordinates{X: 1}, hx.CursorAtScroll())

	send(t, hx, modKey(term.ModAlt, '.'))
	assert.Equal(t, term.Coordinates{X: 3}, hx.CursorAtScroll())
	assert.Equal(t, ",b,", sel(t, hx))

	t.Run("without a prior motion it is unhandled", func(t *testing.T) {
		fresh, _, _ := newHelix(t, "abc", term.Coordinates{})
		_, handled := fresh.Handle(modKey(term.ModAlt, '.'))
		assert.False(t, handled)
	})
}

// TestSelectModeExtends pins v as Helix's sticky select mode: every
// motion keeps the anchor until the mode is left.
func TestSelectModeExtends(t *testing.T) {
	t.Run("word motions accumulate", func(t *testing.T) {
		hx, _, _ := newHelix(t, "foo bar baz", term.Coordinates{})
		send(t, hx, key('v'))
		assert.True(t, hx.IsSelectMode())
		send(t, hx, key('w'))
		assert.Equal(t, "foo ", sel(t, hx))
		send(t, hx, key('w'))
		assert.Equal(t, "foo bar ", sel(t, hx))
		send(t, hx, key('e'))
		assert.Equal(t, "foo bar baz", sel(t, hx))
	})

	t.Run("caret motions accumulate", func(t *testing.T) {
		hx, _, _ := newHelix(t, "abcdef", term.Coordinates{})
		send(t, hx, key('v'))
		send(t, hx, keys("lll")...)
		assert.Equal(t, "abcd", sel(t, hx))
	})

	t.Run("counted motions keep one anchor", func(t *testing.T) {
		hx, _, _ := newHelix(t, "foo bar baz", term.Coordinates{})
		send(t, hx, key('v'))
		send(t, hx, keys("2w")...)
		assert.Equal(t, "foo bar ", sel(t, hx))
	})

	t.Run("vertical motions accumulate", func(t *testing.T) {
		hx, _, _ := newHelix(t, "ab\ncd\nef", term.Coordinates{})
		send(t, hx, key('v'))
		send(t, hx, keys("jj")...)
		assert.Equal(t, "ab\ncd\ne", sel(t, hx))
	})

	t.Run("v toggles back to normal mode", func(t *testing.T) {
		hx, _, _ := newHelix(t, "abcdef", term.Coordinates{})
		send(t, hx, keys("vll")...)
		require.Equal(t, "abc", sel(t, hx))
		send(t, hx, key('v'))
		assert.False(t, hx.IsSelectMode())
		send(t, hx, key('l'))
		assert.Equal(t, "d", sel(t, hx), "normal mode collapses again")
	})

	t.Run("esc leaves select mode but keeps the selection", func(t *testing.T) {
		hx, _, _ := newHelix(t, "abcdef", term.Coordinates{})
		send(t, hx, keys("vll")...)
		send(t, hx, namedKey(term.KeyEsc))
		assert.False(t, hx.IsSelectMode())
		assert.Equal(t, "abc", sel(t, hx))
	})

	t.Run("backwards extension keeps the anchor cell covered", func(t *testing.T) {
		hx, _, _ := newHelix(t, "abcdef", term.Coordinates{X: 3})
		send(t, hx, key('v'))
		send(t, hx, keys("hh")...)
		assert.Equal(t, "bcd", sel(t, hx))
	})

	t.Run("find char extends", func(t *testing.T) {
		hx, _, _ := newHelix(t, "foo,bar", term.Coordinates{})
		send(t, hx, key('v'), key('l'))
		require.Equal(t, "fo", sel(t, hx))
		send(t, hx, key('f'), key('r'))
		assert.Equal(t, "foo,bar", sel(t, hx))
	})

	// put_cursor shifts the anchor by one grapheme when the head
	// crosses it, so the cell the selection started on stays covered.
	t.Run("a word motion flips the selection backwards", func(t *testing.T) {
		hx, _, _ := newHelix(t, "one two three", term.Coordinates{X: 8})
		send(t, hx, key('v'), key('b'))
		assert.Equal(t, "two t", sel(t, hx))
		assert.Equal(t, term.Coordinates{X: 4}, hx.CursorAtScroll())
	})

	t.Run("a backward selection flips forward again", func(t *testing.T) {
		hx, _, _ := newHelix(t, "one two three", term.Coordinates{X: 8})
		send(t, hx, key('v'), key('b'))
		require.Equal(t, "two t", sel(t, hx))
		send(t, hx, key('e'))
		assert.Equal(t, "o t", sel(t, hx), "the head walks back over the anchor")
		send(t, hx, key('e'))
		assert.Equal(t, "three", sel(t, hx))
	})

	t.Run("a collapsing motion keeps a single cell", func(t *testing.T) {
		hx, _, _ := newHelix(t, "one two three", term.Coordinates{X: 8})
		send(t, hx, key('v'), key('l'))
		require.Equal(t, "th", sel(t, hx))
		send(t, hx, key('b'))
		assert.Equal(t, "t", sel(t, hx))
	})

	t.Run("A-; swaps the ends without changing the span", func(t *testing.T) {
		hx, _, _ := newHelix(t, "foo bar", term.Coordinates{})
		send(t, hx, key('v'), key('w'))
		require.Equal(t, "foo ", sel(t, hx))
		send(t, hx, modKey(term.ModAlt, ';'))
		assert.Equal(t, "foo ", sel(t, hx))
		assert.Equal(t, term.Coordinates{}, hx.CursorAtScroll())
	})

	t.Run("A-: forces the head to trail the anchor", func(t *testing.T) {
		hx, _, _ := newHelix(t, "foo bar", term.Coordinates{X: 4})
		send(t, hx, key('v'), key('b'))
		require.Equal(t, "foo b", sel(t, hx))
		send(t, hx, modKey(term.ModAlt, ':'))
		assert.Equal(t, "foo b", sel(t, hx))
		assert.Equal(t, term.Coordinates{X: 4}, hx.CursorAtScroll())
	})

	t.Run("A-: on a forward selection is unhandled", func(t *testing.T) {
		hx, _, _ := newHelix(t, "foo bar", term.Coordinates{})
		send(t, hx, key('v'), key('w'))
		_, handled := hx.Handle(modKey(term.ModAlt, ':'))
		assert.False(t, handled)
	})

	t.Run("an explicit selection is rebound before extending", func(t *testing.T) {
		hx, _, _ := newHelix(t, "foo bar", term.Coordinates{})
		send(t, hx, keys("miw")...)
		require.Equal(t, "foo", sel(t, hx))
		send(t, hx, key('v'))
		send(t, hx, key('l'))
		assert.Equal(t, "foo ", sel(t, hx))
	})
}

// TestCollapseAndFlip pins ; A-; and A-:.
func TestCollapseAndFlip(t *testing.T) {
	t.Run("; collapses onto the caret", func(t *testing.T) {
		hx, _, _ := newHelix(t, "foo bar", term.Coordinates{})
		send(t, hx, keys("w;")...)
		assert.Equal(t, " ", sel(t, hx))
		assert.Equal(t, term.Coordinates{X: 3}, hx.CursorAtScroll())
	})

	t.Run("A-; flips the ends", func(t *testing.T) {
		hx, _, _ := newHelix(t, "foo bar", term.Coordinates{})
		send(t, hx, key('w'))
		require.Equal(t, term.Coordinates{X: 3}, hx.CursorAtScroll())
		send(t, hx, modKey(term.ModAlt, ';'))
		assert.Equal(t, term.Coordinates{}, hx.CursorAtScroll())
		assert.Equal(t, "foo ", sel(t, hx), "flipping must not change the covered text")
	})

	t.Run("A-: normalises a backwards selection", func(t *testing.T) {
		hx, _, _ := newHelix(t, "foo bar", term.Coordinates{X: 6})
		send(t, hx, key('b'))
		require.Equal(t, term.Coordinates{X: 4}, hx.CursorAtScroll())
		send(t, hx, modKey(term.ModAlt, ':'))
		assert.Equal(t, term.Coordinates{X: 6}, hx.CursorAtScroll())
		assert.Equal(t, "bar", sel(t, hx))
	})

	t.Run("A-: on a forward selection is a no-op", func(t *testing.T) {
		hx, _, _ := newHelix(t, "foo bar", term.Coordinates{})
		send(t, hx, key('w'))
		_, handled := hx.Handle(modKey(term.ModAlt, ':'))
		assert.False(t, handled)
		assert.Equal(t, term.Coordinates{X: 3}, hx.CursorAtScroll())
	})
}

// TestLineSelection pins x, X and A-x.
func TestLineSelection(t *testing.T) {
	t.Run("x selects the line", func(t *testing.T) {
		hx, _, _ := newHelix(t, "one\ntwo\nthree", term.Coordinates{})
		send(t, hx, key('x'))
		assert.Equal(t, "one\n", sel(t, hx))
	})

	t.Run("x extends downwards on repeat", func(t *testing.T) {
		hx, _, _ := newHelix(t, "one\ntwo\nthree", term.Coordinates{})
		send(t, hx, keys("xx")...)
		assert.Equal(t, "one\ntwo\n", sel(t, hx))
	})

	t.Run("counted x selects that many lines", func(t *testing.T) {
		hx, _, _ := newHelix(t, "a\nb\nc\nd", term.Coordinates{})
		send(t, hx, keys("3x")...)
		assert.Equal(t, "a\nb\nc\n", sel(t, hx))
	})

	t.Run("x at the last line stays put", func(t *testing.T) {
		hx, _, _ := newHelix(t, "one\ntwo", term.Coordinates{Y: 1})
		send(t, hx, key('x'))
		first := sel(t, hx)
		send(t, hx, key('x'))
		assert.Equal(t, first, sel(t, hx), "no line below to extend onto")
	})

	t.Run("X snaps a partial selection out to whole lines", func(t *testing.T) {
		hx, _, _ := newHelix(t, "one two\nthree", term.Coordinates{})
		send(t, hx, key('w'))
		require.Equal(t, "one ", sel(t, hx))
		send(t, hx, key('X'))
		assert.Equal(t, "one two\n", sel(t, hx))
	})

	t.Run("A-x leaves a single-line selection alone", func(t *testing.T) {
		hx, _, _ := newHelix(t, "one two\nthree", term.Coordinates{})
		send(t, hx, key('w'))
		_, handled := hx.Handle(modKey(term.ModAlt, 'x'))
		assert.False(t, handled, "shrink_to_line_bounds is a no-op within one line")
		assert.Equal(t, "one ", sel(t, hx))
	})

	t.Run("A-x drops partially covered lines", func(t *testing.T) {
		hx, _, _ := newHelix(t, "one\ntwo\nthree", term.Coordinates{X: 1})
		send(t, hx, key('v'))
		send(t, hx, keys("jj")...)
		require.Equal(t, "ne\ntwo\nth", sel(t, hx))
		send(t, hx, modKey(term.ModAlt, 'x'))
		assert.Equal(t, "two\n", sel(t, hx))
	})
}

// TestSelectAll pins %.
func TestSelectAll(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content string
		want    string
	}{
		{name: "multi line", content: "one\ntwo", want: "one\ntwo"},
		{name: "single line", content: "only", want: "only"},
		{name: "trailing newline", content: "one\n", want: "one\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hx, _, _ := newHelix(t, tc.content, term.Coordinates{})
			send(t, hx, key('%'))
			assert.Equal(t, tc.want, sel(t, hx))
		})
	}
}

// TestGotoMode pins the g minor mode against Helix's goto commands.
func TestGotoMode(t *testing.T) {
	runMotionCases(t, []motionCase{
		{name: "gg goes to the first line", content: "a\nb\nc", at: term.Coordinates{Y: 2},
			evs: keys("gg"), wantAt: term.Coordinates{}, wantSel: "a"},
		{name: "ge goes to the last line", content: "a\nb\nc", evs: keys("ge"),
			wantAt: term.Coordinates{Y: 2}, wantSel: "c"},
		{name: "ge skips a blank last line", content: "a\nb\n", evs: keys("ge"),
			wantAt: term.Coordinates{Y: 1}, wantSel: "b"},
		{name: "gh goes to the line start", content: "hello", at: term.Coordinates{X: 3},
			evs: keys("gh"), wantAt: term.Coordinates{}, wantSel: "h"},
		{name: "gl goes to the last cell", content: "hello", evs: keys("gl"),
			wantAt: term.Coordinates{X: 4}, wantSel: "o"},
		{name: "gl on an empty line stays at column zero", content: "\nx",
			evs: keys("gl"), wantAt: term.Coordinates{}, wantSel: ""},
		{name: "gs goes to the first non blank", content: "   hi", evs: keys("gs"),
			wantAt: term.Coordinates{X: 3}, wantSel: "h"},
		{name: "counted gg goes to that line", content: "a\nb\nc\nd", evs: keys("3gg"),
			wantAt: term.Coordinates{Y: 2}, wantSel: "c"},
		{name: "counted gg clamps", content: "a\nb", evs: keys("99gg"),
			wantAt: term.Coordinates{Y: 1}, wantSel: "b"},
		{name: "g| goes to the count-th column", content: "abcdef", evs: keys("4g|"),
			wantAt: term.Coordinates{X: 3}, wantSel: "d"},
		{name: "g| clamps to the line end", content: "ab", evs: keys("9g|"),
			wantAt: term.Coordinates{X: 1}, wantSel: "b"},
		{name: "gj moves one line down", content: "ab\ncd", evs: keys("gj"),
			wantAt: term.Coordinates{Y: 1}, wantSel: "c"},
		{name: "gk moves one line up", content: "ab\ncd", at: term.Coordinates{Y: 1},
			evs: keys("gk"), wantAt: term.Coordinates{}, wantSel: "a"},
	})

	t.Run("goto mode is one shot", func(t *testing.T) {
		hx, _, _ := newHelix(t, "abc\ndef", term.Coordinates{})
		send(t, hx, keys("gh")...)
		assert.True(t, hx.IsNormalMode())
	})

	t.Run("esc cancels goto mode", func(t *testing.T) {
		hx, _, _ := newHelix(t, "abc", term.Coordinates{X: 2})
		send(t, hx, key('g'), namedKey(term.KeyEsc))
		assert.True(t, hx.IsNormalMode())
		assert.Equal(t, term.Coordinates{X: 2}, hx.CursorAtScroll())
	})

	t.Run("unbound goto keys stay unhandled", func(t *testing.T) {
		for _, ch := range []rune{'d', 'y', 'r', 'i', 'f', 'a', 'm', 'n', 'p', 'w'} {
			hx, _, _ := newHelix(t, "abc", term.Coordinates{})
			send(t, hx, key('g'))
			_, handled := hx.Handle(key(ch))
			assert.False(t, handled, "g%c belongs to the command layer", ch)
			assert.True(t, hx.IsNormalMode())
		}
	})

	t.Run("goto extends in select mode", func(t *testing.T) {
		hx, _, _ := newHelix(t, "hello", term.Coordinates{})
		send(t, hx, key('v'))
		send(t, hx, keys("gl")...)
		assert.Equal(t, "hello", sel(t, hx))
	})
}

// TestGotoLine pins G, which Helix makes a no-op without a count.
func TestGotoLine(t *testing.T) {
	t.Run("bare G does nothing", func(t *testing.T) {
		hx, _, _ := newHelix(t, "a\nb\nc", term.Coordinates{})
		_, handled := hx.Handle(key('G'))
		assert.False(t, handled, "goto_line only acts on a count")
		assert.Equal(t, term.Coordinates{}, hx.CursorAtScroll())
	})

	t.Run("counted G jumps to the line", func(t *testing.T) {
		hx, _, _ := newHelix(t, "a\nb\nc", term.Coordinates{})
		send(t, hx, keys("2G")...)
		assert.Equal(t, term.Coordinates{Y: 1}, hx.CursorAtScroll())
		assert.Equal(t, "b", sel(t, hx))
	})

	t.Run("counted G skips a blank last line", func(t *testing.T) {
		hx, _, _ := newHelix(t, "a\nb\n", term.Coordinates{})
		send(t, hx, keys("9G")...)
		assert.Equal(t, term.Coordinates{Y: 1}, hx.CursorAtScroll())
	})
}

// TestHomeEndPageKeys pins the named keys Helix binds in normal mode.
func TestHomeEndPageKeys(t *testing.T) {
	runMotionCases(t, []motionCase{
		{name: "home goes to the line start", content: "hello", at: term.Coordinates{X: 3},
			evs: []term.Event{namedKey(term.KeyHome)}, wantAt: term.Coordinates{}, wantSel: "h"},
		{name: "end goes to the last cell", content: "hello",
			evs: []term.Event{namedKey(term.KeyEnd)}, wantAt: term.Coordinates{X: 4}, wantSel: "o"},
	})

	t.Run("pagedown moves down a screen", func(t *testing.T) {
		hx, _, _ := newHelix(t, strings.Repeat("line\n", 200), term.Coordinates{})
		_, handled := hx.Handle(namedKey(term.KeyPgdn))
		require.True(t, handled)
		assert.Greater(t, hx.CursorAtScroll().Y, 0)
	})

	t.Run("pageup moves up a screen", func(t *testing.T) {
		hx, _, _ := newHelix(t, strings.Repeat("line\n", 200), term.Coordinates{Y: 100})
		_, handled := hx.Handle(namedKey(term.KeyPgup))
		require.True(t, handled)
		assert.Less(t, hx.CursorAtScroll().Y, 100)
	})
}

// TestScrollCommands pins ctrl-b/f/u/d/e/y.
func TestScrollCommands(t *testing.T) {
	content := strings.Repeat("line\n", 200)
	for _, tc := range []struct {
		name string
		ch   rune
		at   term.Coordinates
		down bool
	}{
		{name: "ctrl-f pages down", ch: 'f', down: true},
		{name: "ctrl-d half-pages down", ch: 'd', down: true},
		{name: "ctrl-b pages up", ch: 'b', at: term.Coordinates{Y: 100}},
		{name: "ctrl-u half-pages up", ch: 'u', at: term.Coordinates{Y: 100}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hx, _, _ := newHelix(t, content, tc.at)
			before := hx.CursorAtScroll().Y
			_, handled := hx.Handle(modKey(term.ModCtrl, tc.ch))
			require.True(t, handled)
			if tc.down {
				assert.Greater(t, hx.CursorAtScroll().Y, before)
			} else {
				assert.Less(t, hx.CursorAtScroll().Y, before)
			}
		})
	}

	// C-e/C-y move the viewport; the caret only follows when it would
	// otherwise leave the window, and then by at most one line.
	for _, ch := range []rune{'e', 'y'} {
		t.Run("ctrl-"+string(ch)+" scrolls the viewport", func(t *testing.T) {
			hx, _, _ := newHelix(t, content, term.Coordinates{Y: 50})
			before := hx.CursorAtScroll()
			offset := hx.SeekOffset()
			_, handled := hx.Handle(modKey(term.ModCtrl, ch))
			require.True(t, handled)
			assert.NotEqual(t, offset, hx.SeekOffset(), "viewport must move")
			assert.LessOrEqual(t, abs(hx.CursorAtScroll().Y-before.Y), 1,
				"caret follows the viewport by at most one line")
		})
	}
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// TestViewMode pins z and the sticky Z variant.
func TestViewMode(t *testing.T) {
	content := strings.Repeat("line\n", 200)

	t.Run("zz centers without moving", func(t *testing.T) {
		hx, _, _ := newHelix(t, content, term.Coordinates{Y: 100})
		before := hx.CursorAtScroll()
		send(t, hx, keys("zz")...)
		assert.Equal(t, before, hx.CursorAtScroll())
		assert.True(t, hx.IsNormalMode(), "z is one shot")
	})

	for _, ch := range []rune{'z', 'c', 't', 'b', 'm'} {
		t.Run("z"+string(ch)+" realigns the view", func(t *testing.T) {
			hx, _, _ := newHelix(t, content, term.Coordinates{Y: 100})
			before := hx.CursorAtScroll()
			send(t, hx, key('z'))
			send(t, hx, key(ch))
			assert.Equal(t, before, hx.CursorAtScroll())
			assert.True(t, hx.IsNormalMode(), "z is one shot")
		})
	}

	t.Run("zj and zk scroll", func(t *testing.T) {
		for _, ch := range []rune{'j', 'k'} {
			hx, _, _ := newHelix(t, content, term.Coordinates{Y: 100})
			send(t, hx, key('z'))
			_, handled := hx.Handle(key(ch))
			assert.True(t, handled, "z%c must scroll", ch)
		}
	})

	t.Run("z page keys", func(t *testing.T) {
		for _, ev := range []term.Event{
			modKey(term.ModCtrl, 'f'), modKey(term.ModCtrl, 'b'),
			modKey(term.ModCtrl, 'd'), modKey(term.ModCtrl, 'u'),
			namedKey(term.KeyPgdn), namedKey(term.KeyPgup),
			namedKey(term.KeySpace), namedKey(term.KeyBackspace),
		} {
			hx, _, _ := newHelix(t, content, term.Coordinates{Y: 100})
			send(t, hx, key('z'))
			_, handled := hx.Handle(ev)
			assert.True(t, handled, "z + %v must page", ev)
		}
	})

	t.Run("Z stays in view mode until esc", func(t *testing.T) {
		hx, _, _ := newHelix(t, content, term.Coordinates{Y: 100})
		send(t, hx, key('Z'))
		assert.False(t, hx.IsNormalMode())
		send(t, hx, key('t'))
		assert.False(t, hx.IsNormalMode(), "Z is sticky")
		send(t, hx, key('b'))
		assert.False(t, hx.IsNormalMode())
		send(t, hx, namedKey(term.KeyEsc))
		assert.True(t, hx.IsNormalMode())
	})

	t.Run("zn and zN repeat the search", func(t *testing.T) {
		hx, _, _ := newHelix(t, "foo\nbar\nfoo", term.Coordinates{})
		send(t, hx, key('/'))
		send(t, hx, keys("foo")...)
		send(t, hx, namedKey(term.KeyEnter))
		require.Equal(t, 2, hx.CursorAtScroll().Y)

		send(t, hx, keys("zn")...)
		assert.Equal(t, 0, hx.CursorAtScroll().Y)
		send(t, hx, keys("zN")...)
		assert.Equal(t, 2, hx.CursorAtScroll().Y)
	})

	t.Run("z/ opens the search prompt and leaves view mode", func(t *testing.T) {
		hx, _, _ := newHelix(t, "foo bar", term.Coordinates{})
		send(t, hx, keys("z/")...)
		assert.True(t, hx.IsSearchMode())
	})

	t.Run("z? opens the reverse search prompt", func(t *testing.T) {
		hx, _, _ := newHelix(t, "foo bar", term.Coordinates{})
		send(t, hx, keys("z?")...)
		assert.True(t, hx.IsSearchMode())
	})

	t.Run("an unbound view key leaves view mode", func(t *testing.T) {
		hx, _, _ := newHelix(t, "foo bar", term.Coordinates{})
		send(t, hx, key('z'))
		_, handled := hx.Handle(modKey(term.ModCtrl, 'g'))
		assert.False(t, handled)
		assert.True(t, hx.IsNormalMode())
	})
}

// TestJumplist pins ctrl-s / ctrl-o / ctrl-i.
func TestJumplist(t *testing.T) {
	t.Run("ctrl-o returns to a saved selection", func(t *testing.T) {
		hx, _, _ := newHelix(t, "a\nb\nc\nd\ne", term.Coordinates{})
		send(t, hx, modKey(term.ModCtrl, 's'))
		send(t, hx, keys("3j")...)
		send(t, hx, modKey(term.ModCtrl, 's'))
		require.Equal(t, term.Coordinates{Y: 3}, hx.CursorAtScroll())

		send(t, hx, modKey(term.ModCtrl, 'o'))
		assert.Equal(t, term.Coordinates{}, hx.CursorAtScroll())
	})

	t.Run("ctrl-i walks forward again", func(t *testing.T) {
		hx, _, _ := newHelix(t, "a\nb\nc\nd\ne", term.Coordinates{})
		send(t, hx, modKey(term.ModCtrl, 's'))
		send(t, hx, keys("3j")...)
		send(t, hx, modKey(term.ModCtrl, 's'))
		send(t, hx, modKey(term.ModCtrl, 'o'))
		send(t, hx, modKey(term.ModCtrl, 'i'))
		assert.Equal(t, term.Coordinates{Y: 3}, hx.CursorAtScroll())
	})

	t.Run("tab is an alias for ctrl-i", func(t *testing.T) {
		hx, _, _ := newHelix(t, "a\nb\nc", term.Coordinates{})
		send(t, hx, modKey(term.ModCtrl, 's'))
		send(t, hx, keys("2j")...)
		send(t, hx, modKey(term.ModCtrl, 's'))
		send(t, hx, modKey(term.ModCtrl, 'o'))
		send(t, hx, namedKey(term.KeyTab))
		assert.Equal(t, term.Coordinates{Y: 2}, hx.CursorAtScroll())
	})

	t.Run("an empty jumplist is unhandled", func(t *testing.T) {
		hx, _, _ := newHelix(t, "a\nb", term.Coordinates{})
		_, handled := hx.Handle(modKey(term.ModCtrl, 'o'))
		assert.False(t, handled)
		_, handled = hx.Handle(modKey(term.ModCtrl, 'i'))
		assert.False(t, handled)
	})

	t.Run("gg and ge push a jump", func(t *testing.T) {
		hx, _, _ := newHelix(t, "a\nb\nc\nd", term.Coordinates{Y: 2})
		send(t, hx, keys("gg")...)
		require.Equal(t, term.Coordinates{}, hx.CursorAtScroll())
		send(t, hx, modKey(term.ModCtrl, 'o'))
		assert.Equal(t, term.Coordinates{Y: 2}, hx.CursorAtScroll())
	})

	// push_jump also fires for the explicit jumps G, g|, g. and search.
	for _, tc := range []struct {
		name string
		evs  []term.Event
		want term.Coordinates
	}{
		{name: "counted G", evs: keys("4G"), want: term.Coordinates{Y: 3}},
		{name: "counted gg", evs: keys("4gg"), want: term.Coordinates{Y: 3}},
		{name: "g|", evs: keys("3g|"), want: term.Coordinates{X: 2, Y: 2}},
	} {
		t.Run(tc.name+" pushes a jump", func(t *testing.T) {
			hx, _, _ := newHelix(t, "aaa\nbbb\nccc\nddd", term.Coordinates{Y: 2})
			send(t, hx, tc.evs...)
			require.Equal(t, tc.want, hx.CursorAtScroll())
			send(t, hx, modKey(term.ModCtrl, 'o'))
			assert.Equal(t, term.Coordinates{Y: 2}, hx.CursorAtScroll())
		})
	}

	t.Run("a search pushes a jump", func(t *testing.T) {
		hx, _, _ := newHelix(t, "foo\nbar\nbaz", term.Coordinates{})
		send(t, hx, key('/'))
		send(t, hx, keys("baz")...)
		send(t, hx, namedKey(term.KeyEnter))
		require.Equal(t, 2, hx.CursorAtScroll().Y)
		send(t, hx, modKey(term.ModCtrl, 'o'))
		assert.Equal(t, term.Coordinates{}, hx.CursorAtScroll())
	})

	t.Run("n pushes a jump", func(t *testing.T) {
		hx, _, _ := newHelix(t, "foo\nbar\nfoo", term.Coordinates{})
		hx.Search("foo")
		send(t, hx, key('n'))
		require.Equal(t, 2, hx.CursorAtScroll().Y)
		send(t, hx, modKey(term.ModCtrl, 'o'))
		assert.Equal(t, term.Coordinates{}, hx.CursorAtScroll())
	})

	t.Run("g. pushes a jump", func(t *testing.T) {
		hx, _, _ := newHelix(t, "a\nb\nc\nd", term.Coordinates{Y: 3})
		send(t, hx, key('d'))
		send(t, hx, keys("gg")...)
		require.Equal(t, term.Coordinates{}, hx.CursorAtScroll())
		send(t, hx, keys("g.")...)
		require.Equal(t, 3, hx.CursorAtScroll().Y)
		send(t, hx, modKey(term.ModCtrl, 'o'))
		assert.Equal(t, term.Coordinates{}, hx.CursorAtScroll())
	})
}

// TestBracketMode pins the [ and ] minor modes.
func TestBracketMode(t *testing.T) {
	t.Run("]p moves to the next paragraph", func(t *testing.T) {
		hx, _, _ := newHelix(t, "a\n\nb\n\nc", term.Coordinates{})
		send(t, hx, keys("]p")...)
		assert.Greater(t, hx.CursorAtScroll().Y, 0)
		assert.True(t, hx.IsNormalMode())
	})

	t.Run("[p moves to the previous paragraph", func(t *testing.T) {
		hx, _, _ := newHelix(t, "a\n\nb\n\nc", term.Coordinates{Y: 4})
		send(t, hx, keys("[p")...)
		assert.Less(t, hx.CursorAtScroll().Y, 4)
	})

	t.Run("]<space> adds a line below", func(t *testing.T) {
		hx, buf, _ := newHelix(t, "a\nb", term.Coordinates{})
		send(t, hx, key(']'), namedKey(term.KeySpace))
		assert.Equal(t, "a\n\nb", buf.String())
		assert.Equal(t, term.Coordinates{}, hx.CursorAtScroll())
		assert.True(t, hx.IsNormalMode())
	})

	t.Run("[<space> adds a line above", func(t *testing.T) {
		hx, buf, _ := newHelix(t, "a\nb", term.Coordinates{})
		send(t, hx, key('['), namedKey(term.KeySpace))
		assert.Equal(t, "\na\nb", buf.String())
		assert.Equal(t, term.Coordinates{Y: 1}, hx.CursorAtScroll())
	})

	t.Run("unbound bracket keys stay unhandled", func(t *testing.T) {
		for _, ch := range []rune{'d', 'g', 'f', 't', 'a', 'c', 'e', 'x'} {
			hx, _, _ := newHelix(t, "abc", term.Coordinates{})
			send(t, hx, key(']'))
			_, handled := hx.Handle(key(ch))
			assert.False(t, handled, "]%c belongs to the command layer", ch)
			assert.True(t, hx.IsNormalMode())
		}
	})
}

// TestMatchMode pins mm and the mi/ma text objects.
func TestMatchMode(t *testing.T) {
	t.Run("mm jumps to the matching bracket", func(t *testing.T) {
		hx, _, _ := newHelix(t, "(abc)", term.Coordinates{})
		send(t, hx, keys("mm")...)
		assert.Equal(t, term.Coordinates{X: 4}, hx.CursorAtScroll())
		assert.Equal(t, ")", sel(t, hx))
	})

	t.Run("mm jumps back", func(t *testing.T) {
		hx, _, _ := newHelix(t, "(abc)", term.Coordinates{X: 4})
		send(t, hx, keys("mm")...)
		assert.Equal(t, term.Coordinates{}, hx.CursorAtScroll())
	})

	for _, tc := range []struct {
		name    string
		content string
		at      term.Coordinates
		obj     rune
		around  bool
		want    string
	}{
		{name: "inner paren", content: "f(abc)", at: term.Coordinates{X: 3}, obj: '(', want: "abc"},
		{name: "around paren", content: "f(abc)", at: term.Coordinates{X: 3}, obj: '(', around: true, want: "(abc)"},
		{name: "inner brace", content: "x{ab}", at: term.Coordinates{X: 2}, obj: '{', want: "ab"},
		{name: "inner bracket", content: "x[ab]", at: term.Coordinates{X: 2}, obj: '[', want: "ab"},
		{name: "inner quote", content: `x = "abc"`, at: term.Coordinates{X: 6}, obj: '"', want: "abc"},
		{name: "around quote", content: `x = "abc"`, at: term.Coordinates{X: 6}, obj: '"', around: true, want: `"abc"`},
		{name: "inner word", content: "foo bar", at: term.Coordinates{X: 5}, obj: 'w', want: "bar"},
		{name: "b alias for paren", content: "f(abc)", at: term.Coordinates{X: 3}, obj: 'b', want: "abc"},
		{name: "B alias for brace", content: "x{ab}", at: term.Coordinates{X: 2}, obj: 'B', want: "ab"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hx, _, _ := newHelix(t, tc.content, tc.at)
			inner := 'i'
			if tc.around {
				inner = 'a'
			}
			send(t, hx, key('m'), key(inner), key(tc.obj))
			assert.Equal(t, tc.want, sel(t, hx))
			assert.True(t, hx.IsNormalMode())
		})
	}

	t.Run("an unknown text object leaves the selection alone", func(t *testing.T) {
		hx, _, _ := newHelix(t, "abc", term.Coordinates{})
		send(t, hx, key('m'), key('i'), key('Z'))
		assert.Equal(t, "a", sel(t, hx))
		assert.True(t, hx.IsNormalMode())
	})

	t.Run("esc cancels match mode", func(t *testing.T) {
		hx, _, _ := newHelix(t, "abc", term.Coordinates{})
		send(t, hx, key('m'), namedKey(term.KeyEsc))
		assert.True(t, hx.IsNormalMode())
	})

	t.Run("a text object selection is operable", func(t *testing.T) {
		hx, buf, _ := newHelix(t, "f(abc)", term.Coordinates{X: 3})
		send(t, hx, keys("mi(d")...)
		assert.Equal(t, "f()", buf.String())
	})

	t.Run("a text object selection can be extended", func(t *testing.T) {
		hx, _, _ := newHelix(t, "(ab) cd", term.Coordinates{X: 1})
		send(t, hx, keys("mi(")...)
		require.Equal(t, "ab", sel(t, hx))
		send(t, hx, key('v'), key('l'))
		assert.Equal(t, "ab)", sel(t, hx))
	})
}

// TestSearch pins / ? n N and the search-selection commands.
func TestSearch(t *testing.T) {
	t.Run("forward search selects the match", func(t *testing.T) {
		hx, _, _ := newHelix(t, "foo\nbar\nfoo", term.Coordinates{})
		send(t, hx, key('/'))
		require.True(t, hx.IsSearchMode())
		send(t, hx, keys("foo")...)
		send(t, hx, namedKey(term.KeyEnter))
		require.False(t, hx.IsSearchMode())
		assert.Equal(t, term.Coordinates{X: 2, Y: 2}, hx.CursorAtScroll(),
			"confirming the prompt jumps to the next match")
		assert.Equal(t, "foo", sel(t, hx))

		send(t, hx, key('n'))
		assert.Equal(t, term.Coordinates{X: 2}, hx.CursorAtScroll(), "n wraps")
		assert.Equal(t, "foo", sel(t, hx))
	})

	t.Run("N walks backwards", func(t *testing.T) {
		hx, _, _ := newHelix(t, "foo\nbar\nfoo", term.Coordinates{})
		send(t, hx, key('/'))
		send(t, hx, keys("foo")...)
		send(t, hx, namedKey(term.KeyEnter))
		send(t, hx, key('n'))
		require.Equal(t, term.Coordinates{X: 2}, hx.CursorAtScroll())
		send(t, hx, key('N'))
		assert.Equal(t, term.Coordinates{X: 2, Y: 2}, hx.CursorAtScroll())
		assert.Equal(t, "foo", sel(t, hx))
	})

	t.Run("? reverses the meaning of n", func(t *testing.T) {
		hx, _, _ := newHelix(t, "foo\nbar\nfoo", term.Coordinates{Y: 2})
		send(t, hx, key('?'))
		require.True(t, hx.IsSearchMode())
		send(t, hx, keys("foo")...)
		send(t, hx, namedKey(term.KeyEnter))
		require.Equal(t, 0, hx.CursorAtScroll().Y)
		send(t, hx, key('n'))
		assert.Equal(t, 2, hx.CursorAtScroll().Y, "n keeps searching backwards")
	})

	t.Run("* searches the selected word", func(t *testing.T) {
		hx, _, _ := newHelix(t, "foo bar\nfoo", term.Coordinates{})
		send(t, hx, keys("mi")...)
		send(t, hx, key('w'))
		require.Equal(t, "foo", sel(t, hx))
		_, handled := hx.Handle(key('*'))
		require.True(t, handled)
		send(t, hx, key('n'))
		assert.Equal(t, 1, hx.CursorAtScroll().Y)
	})

	t.Run("A-* searches the raw selection", func(t *testing.T) {
		hx, _, _ := newHelix(t, "foobar\nxfoo", term.Coordinates{})
		send(t, hx, keys("2l")...)
		send(t, hx, key('v'))
		send(t, hx, key('h'))
		_, handled := hx.Handle(modKey(term.ModAlt, '*'))
		assert.True(t, handled)
	})

	t.Run("search is disabled by option", func(t *testing.T) {
		hx, _, _ := newHelix(t, "foo", term.Coordinates{}, WithSearch(false))
		_, handled := hx.Handle(key('/'))
		assert.False(t, handled)
		assert.False(t, hx.IsSearchMode())
	})

	t.Run("n without a search is unhandled", func(t *testing.T) {
		hx, _, _ := newHelix(t, "foo", term.Coordinates{})
		_, handled := hx.Handle(key('n'))
		assert.False(t, handled)
	})
}

// TestExpandShrinkSelection pins A-o / A-i and their arrow aliases,
// which need a syntax service and therefore stay unhandled here.
func TestExpandShrinkSelection(t *testing.T) {
	for _, ev := range []term.Event{
		modKey(term.ModAlt, 'o'), modKey(term.ModAlt, 'i'),
		modNamedKey(term.ModAlt, term.KeyArrowUp),
		modNamedKey(term.ModAlt, term.KeyArrowDown),
	} {
		hx, _, _ := newHelix(t, "f(abc)", term.Coordinates{X: 3})
		_, handled := hx.Handle(ev)
		assert.False(t, handled, "%v needs a syntax service", ev)
	}

	// With a syntax tree the caret's one-cell range grows to the node
	// around it and shrinks back.
	// An unexpanded caret is reported as a zero-width range.
	caret := term.Range{
		Start: term.Coordinates{X: 3}, End: term.Coordinates{X: 3}}
	single := term.Range{
		Start: term.Coordinates{X: 3}, End: term.Coordinates{X: 4}}
	inner := term.Range{
		Start: term.Coordinates{X: 2}, End: term.Coordinates{X: 5}}
	outer := term.Range{
		Start: term.Coordinates{X: 1}, End: term.Coordinates{X: 6}}

	newSyntaxHelix := func(t *testing.T) *Helix {
		t.Helper()
		hx, buf, _ := newHelix(t, "f(abc)", term.Coordinates{X: 3})
		buf.WithView(testSelectionView{
			View:   buf.View(),
			expand: map[term.Range]term.Range{caret: inner, inner: outer},
			shrink: map[term.Range]term.Range{outer: inner, inner: single},
		})
		return hx
	}

	for _, tc := range []struct {
		name   string
		expand term.Event
		shrink term.Event
	}{
		{name: "alt letters", expand: modKey(term.ModAlt, 'o'),
			shrink: modKey(term.ModAlt, 'i')},
		{name: "alt arrows",
			expand: modNamedKey(term.ModAlt, term.KeyArrowUp),
			shrink: modNamedKey(term.ModAlt, term.KeyArrowDown)},
	} {
		t.Run(tc.name+" expand and shrink", func(t *testing.T) {
			hx := newSyntaxHelix(t)
			send(t, hx, tc.expand)
			assert.Equal(t, "abc", sel(t, hx))
			send(t, hx, tc.expand)
			assert.Equal(t, "(abc)", sel(t, hx))
			send(t, hx, tc.shrink)
			assert.Equal(t, "abc", sel(t, hx))
		})
	}

	t.Run("an expanded selection is pinned until a motion rebinds it", func(t *testing.T) {
		hx := newSyntaxHelix(t)
		send(t, hx, modKey(term.ModAlt, 'o'))
		require.Equal(t, "abc", sel(t, hx))
		send(t, hx, key('d'))
		assert.Equal(t, "f()", hx.buf.String())
	})
}
