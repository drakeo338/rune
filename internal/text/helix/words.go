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
	"unicode"

	"github.com/unstablebuild/rune-go-sdk/term"
)

// Character categories from helix-core/src/chars.rs. Word motions
// decide where a range starts by asking whether the cell next to the
// caret is already the boundary the motion is looking for, and that
// question is answered with these categories.
type charCategory uint8

const (
	catEOL charCategory = iota
	catWhitespace
	catWord
	catPunctuation
	catUnknown
)

func categorizeChar(ch rune) charCategory {
	switch {
	case ch == '\n' || ch == '\r':
		return catEOL
	case unicode.IsSpace(ch):
		return catWhitespace
	case unicode.IsLetter(ch) || unicode.IsDigit(ch) || ch == '_':
		return catWord
	case ch < 128 && (unicode.IsPunct(ch) || unicode.IsSymbol(ch)):
		return catPunctuation
	default:
		return catUnknown
	}
}

func isWordBoundary(a, b rune) bool {
	return categorizeChar(a) != categorizeChar(b)
}

// isLongWordBoundary treats words and punctuation as the same material,
// which is what makes W/B/E span "a.b".
func isLongWordBoundary(a, b rune) bool {
	ca, cb := categorizeChar(a), categorizeChar(b)
	switch {
	case ca == catWord && cb == catPunctuation,
		ca == catPunctuation && cb == catWord:
		return false
	default:
		return ca != cb
	}
}

func isLineEnding(ch rune) bool { return ch == '\n' || ch == '\r' }

// wordMotionTarget mirrors helix-core's WordMotionTarget for the six
// motions w/W, e/E and b/B bind to.
type wordMotionTarget uint8

const (
	nextWordStart wordMotionTarget = iota
	nextWordEnd
	nextLongWordStart
	nextLongWordEnd
	prevWordStart
	prevLongWordStart
)

func (t wordMotionTarget) backward() bool {
	return t == prevWordStart || t == prevLongWordStart
}

// reachedTarget is helix-core/src/movement.rs reached_target.
func reachedTarget(target wordMotionTarget, prev, next rune) bool {
	switch target {
	case nextWordStart:
		return isWordBoundary(prev, next) &&
			(isLineEnding(next) || !unicode.IsSpace(next))
	case nextWordEnd, prevWordStart:
		return isWordBoundary(prev, next) &&
			(!unicode.IsSpace(prev) || isLineEnding(next))
	case nextLongWordStart:
		return isLongWordBoundary(prev, next) &&
			(isLineEnding(next) || !unicode.IsSpace(next))
	default:
		return isLongWordBoundary(prev, next) &&
			(!unicode.IsSpace(prev) || isLineEnding(next))
	}
}

// Document space. Helix walks a rope where every line ends in a real
// newline character, so a position runs over [0, Columns(y)] for each
// row -- X == Columns(y) is the line ending -- plus one position past
// the final newline, spelled {X: 0, Y: Rows()}, which is where a
// forward motion comes to rest at the end of the buffer.

func (h *helixHandlerImpl) docEnd() term.Coordinates {
	return term.Coordinates{Y: h.less.Buffer().Rows()}
}

// charAt returns the document character at pos, reporting the line
// ending for the slot past the last cell of a row.
func (h *helixHandlerImpl) charAt(pos term.Coordinates) (rune, bool) {
	buf := h.less.Buffer()
	if pos.Y < 0 || pos.Y >= buf.Rows() || pos.X < 0 {
		return 0, false
	}
	cells := buf.View().RawCells()
	if pos.Y >= len(cells) {
		return 0, false
	}
	row := cells[pos.Y]
	switch {
	case pos.X < len(row):
		return row[pos.X].Ch, true
	case pos.X == len(row):
		return '\n', true
	}
	return 0, false
}

func (h *helixHandlerImpl) nextPos(pos term.Coordinates) (term.Coordinates, bool) {
	buf := h.less.Buffer()
	if pos.Y < 0 || pos.Y >= buf.Rows() {
		return pos, false
	}
	if pos.X < buf.Columns(pos.Y) {
		pos.X++
		return pos, true
	}
	return term.Coordinates{Y: pos.Y + 1}, true
}

func (h *helixHandlerImpl) prevPos(pos term.Coordinates) (term.Coordinates, bool) {
	if pos.X > 0 {
		pos.X--
		return pos, true
	}
	if pos.Y <= 0 {
		return pos, false
	}
	return term.Coordinates{X: h.less.Buffer().Columns(pos.Y - 1), Y: pos.Y - 1}, true
}

// docPos maps a row-local position onto document space. The cursor
// reports a selection edge one column past the caret, which for a caret
// on a line ending - and so for every caret on a blank row - lands past
// the last position the row has. The equivalent document position is
// the start of the next row.
func (h *helixHandlerImpl) docPos(pos term.Coordinates) term.Coordinates {
	buf := h.less.Buffer()
	if pos.Y < 0 || pos.Y >= buf.Rows() || pos.X <= buf.Columns(pos.Y) {
		return pos
	}
	return term.Coordinates{Y: pos.Y + 1}
}

// clampCell maps a document position back onto a real buffer cell, so
// the line-ending slot and the end of the document resolve to the last
// cell the caret can occupy.
func (h *helixHandlerImpl) clampCell(pos term.Coordinates) term.Coordinates {
	buf := h.less.Buffer()
	rows := buf.Rows()
	if rows == 0 {
		return term.Coordinates{}
	}
	if pos.Y >= rows {
		pos.Y = rows - 1
		pos.X = buf.Columns(pos.Y)
	}
	pos.Y = max(0, pos.Y)
	pos.X = max(0, min(pos.X, buf.Columns(pos.Y)-1))
	return pos
}

// wordMove is helix-core/src/movement.rs word_move: normalise the range
// to a one-cell block cursor facing the motion direction, then scan for
// the target count times.
func (h *helixHandlerImpl) wordMove(
	anchor, head term.Coordinates, count int, target wordMotionTarget,
) (term.Coordinates, term.Coordinates) {
	back := target.backward()
	if back && head == (term.Coordinates{}) {
		return anchor, head
	}
	if !back && head == h.docEnd() {
		return anchor, head
	}

	prev, hasPrev := h.prevPos(head)
	if !hasPrev {
		prev = head
	}
	next, hasNext := h.nextPos(head)
	if !hasNext {
		next = head
	}
	var a, hd term.Coordinates
	switch {
	case back && coordinatesBefore(anchor, head):
		a, hd = head, prev
	case back:
		a, hd = next, head
	case coordinatesBefore(anchor, head):
		a, hd = prev, head
	default:
		a, hd = head, next
	}

	for range max(1, count) {
		na, nhd := h.rangeToTarget(target, a, hd)
		if na == a && nhd == hd {
			break
		}
		a, hd = na, nhd
	}
	return a, hd
}

// rangeToTarget is CharHelpers::range_to_target. The Rust version walks
// a bidirectional rope cursor; here the cursor position and head always
// coincide, so the character the iterator would yield is simply the one
// at head (forward) or just before it (backward).
func (h *helixHandlerImpl) rangeToTarget(
	target wordMotionTarget, originAnchor, originHead term.Coordinates,
) (term.Coordinates, term.Coordinates) {
	back := target.backward()
	yield := func(head term.Coordinates) (rune, bool) {
		if !back {
			return h.charAt(head)
		}
		p, ok := h.prevPos(head)
		if !ok {
			return 0, false
		}
		return h.charAt(p)
	}
	advance := func(head term.Coordinates) term.Coordinates {
		var next term.Coordinates
		var ok bool
		if back {
			next, ok = h.prevPos(head)
		} else {
			next, ok = h.nextPos(head)
		}
		if !ok {
			return head
		}
		return next
	}

	anchor, head := originAnchor, originHead
	var prevCh rune
	var hasPrev bool
	if back {
		prevCh, hasPrev = h.charAt(head)
	} else if p, ok := h.prevPos(head); ok {
		prevCh, hasPrev = h.charAt(p)
	}

	for {
		ch, ok := yield(head)
		if !ok || !isLineEnding(ch) {
			break
		}
		prevCh, hasPrev = ch, true
		head = advance(head)
	}
	if hasPrev && isLineEnding(prevCh) {
		anchor = head
	}

	headStart := head
	for {
		nextCh, ok := yield(head)
		if !ok {
			break
		}
		if !hasPrev || reachedTarget(target, prevCh, nextCh) {
			if head != headStart {
				break
			}
			anchor = head
		}
		prevCh, hasPrev = nextCh, true
		head = advance(head)
	}
	return anchor, head
}
