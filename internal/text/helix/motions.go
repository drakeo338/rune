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
	"github.com/unstablebuild/rune-go-sdk/term"
)

// Helix's selection grammar, mirroring helix-core/src/movement.rs and
// Range::put_cursor in helix-core/src/selection.rs.
//
// Every range is an (anchor, head) pair and is never empty: Helix stores
// selections through Selection::ensure_invariants, which widens a
// collapsed range to one grapheme. text.Cursor with
// RightInclusiveSemantics has the same shape, so a motion is a matter of
// deciding where the anchor lands before moving the head.
//
// In normal mode put_cursor drops a fresh anchor on the landing cell; in
// select mode the existing anchor survives. Word motions additionally
// re-anchor one cell along when the caret already sits on the boundary
// the motion looks for, which is what makes repeated presses walk
// forward instead of standing still (word_move's `head == head_start`
// branch).

// nextCoord returns the buffer position one cell after pos, wrapping to
// the next row. ok is false at the end of the buffer.
func (h *helixHandlerImpl) nextCoord(pos term.Coordinates) (term.Coordinates, bool) {
	buf := h.less.Buffer()
	rows := buf.Rows()
	if pos.Y < 0 || pos.Y >= rows {
		return pos, false
	}
	if pos.X+1 <= buf.Columns(pos.Y)-1 {
		pos.X++
		return pos, true
	}
	if pos.Y+1 >= rows {
		return pos, false
	}
	return term.Coordinates{Y: pos.Y + 1}, true
}

func coordinatesBefore(a, b term.Coordinates) bool {
	return a.Y < b.Y || (a.Y == b.Y && a.X < b.X)
}

// anchorHere drops a fresh anchor on the caret, collapsing the selection
// to the single cell under it.
func (h *helixHandlerImpl) anchorHere() {
	h.cursor.Unselect()
	h.cursor.Select()
	h.explicitSel = false
}

// setSelectionRange rebuilds the selection as the (anchor, head) pair,
// leaving the caret on head.
func (h *helixHandlerImpl) setSelectionRange(anchor, head term.Coordinates) {
	h.cursor.Unselect()
	h.explicitSel = false
	h.cursor.MoveToScroll(anchor)
	h.cursor.Select()
	h.cursor.MoveToScroll(head)
}

// selectionAnchor returns the cell the selection is anchored on.
func (h *helixHandlerImpl) selectionAnchor() (term.Coordinates, bool) {
	anchor, _, ok := h.cursor.SelectionBounds()
	return anchor, ok
}

func (h *helixHandlerImpl) selectionBackward() bool {
	anchor, head, ok := h.cursor.SelectionBounds()
	return ok && coordinatesBefore(head, anchor)
}

// rebindExplicitSelection turns a pinned range (a text object, select
// all, a syntax expansion) back into an anchor/head pair so the next
// motion can extend it.
func (h *helixHandlerImpl) rebindExplicitSelection() {
	from, to, ok := h.cursor.SelectionRange()
	if !ok {
		h.anchorHere()
		return
	}
	last := to
	if last.X > 0 {
		last.X--
	}
	h.setSelectionRange(from, last)
}

// beginMotion prepares the selection for a select-mode motion.
func (h *helixHandlerImpl) beginMotion() {
	if h.explicitSel {
		h.rebindExplicitSelection()
		return
	}
	if _, ok := h.cursor.SelectionMode(); !ok {
		h.cursor.Select()
	}
}

// runMotion applies step count times. Select mode restores the original
// anchor afterwards, matching extend_word_impl: the head is computed as
// if the motion were unextended, then put back on the old anchor.
func (h *helixHandlerImpl) runMotion(step func() bool) bool {
	return h.applyMotion(step, h.motionCount())
}

// runMotionOnce is for motions that consume the count themselves.
func (h *helixHandlerImpl) runMotionOnce(step func() bool) bool {
	return h.applyMotion(step, 1)
}

func (h *helixHandlerImpl) applyMotion(step func() bool, times int) bool {
	run := func() bool {
		moved := false
		for range max(1, times) {
			if !step() {
				break
			}
			moved = true
		}
		return moved
	}
	if !h.extend {
		return run()
	}
	h.beginMotion()
	anchor, ok := h.selectionAnchor()
	if !ok {
		return run()
	}
	moved := run()
	h.setSelectionRange(anchor, h.cursor.CursorAtScroll())
	return moved
}

// moveCaret runs a motion that leaves a single-cell selection behind in
// normal mode: put_cursor(extend=false) collapses to the landing cell.
func (h *helixHandlerImpl) moveCaret(fn func() bool) bool {
	return h.runMotion(h.caretStep(fn))
}

// moveCaretOnce is moveCaret for motions that apply the count inside fn.
func (h *helixHandlerImpl) moveCaretOnce(fn func() bool) bool {
	return h.runMotionOnce(h.caretStep(fn))
}

func (h *helixHandlerImpl) caretStep(fn func() bool) func() bool {
	return func() bool {
		h.cursor.Unselect()
		h.explicitSel = false
		ok := h.moved(fn)
		h.anchorHere()
		return ok
	}
}

// moved runs a cursor primitive and reports whether the caret actually
// changed position. The primitives disagree on what their boolean
// means: Cursor.MoveRightEndWord returns false under
// RightInclusiveSemantics even when it moved, so position is the only
// reliable signal.
func (h *helixHandlerImpl) moved(fn func() bool) bool {
	before := h.cursor.CursorAtScroll()
	fn()
	return h.cursor.CursorAtScroll() != before
}

// helixRange reads the current selection as a Helix (anchor, head) pair
// in document space, where head is the exclusive edge of the range.
func (h *helixHandlerImpl) helixRange() (anchor, head term.Coordinates) {
	from, to, ok := h.cursor.SelectionRange()
	if !ok {
		caret := h.cursor.CursorAtScroll()
		next, ok := h.nextPos(caret)
		if !ok {
			next = caret
		}
		return caret, next
	}
	if h.selectionBackward() {
		return h.docPos(to), from
	}
	return from, h.docPos(to)
}

// applyHelixRange installs a Helix (anchor, head) pair as the cursor's
// inclusive cell selection.
func (h *helixHandlerImpl) applyHelixRange(anchor, head term.Coordinates) {
	switch {
	case coordinatesBefore(anchor, head):
		last, ok := h.prevPos(head)
		if !ok {
			last = anchor
		}
		h.setSelectionRange(h.clampCell(anchor), h.clampCell(last))
	case coordinatesBefore(head, anchor):
		first, ok := h.prevPos(anchor)
		if !ok {
			first = head
		}
		h.setSelectionRange(h.clampCell(first), h.clampCell(head))
	default:
		caret := h.clampCell(head)
		h.setSelectionRange(caret, caret)
	}
}

// rangeCursor is Range::cursor: the block cursor sits on the last cell
// the range covers.
func (h *helixHandlerImpl) rangeCursor(anchor, head term.Coordinates) term.Coordinates {
	if !coordinatesBefore(anchor, head) {
		return head
	}
	prev, ok := h.prevPos(head)
	if !ok {
		return head
	}
	return prev
}

// putCursor is Range::put_cursor with extend set: the anchor survives,
// shifting by one cell when the range flips direction.
func (h *helixHandlerImpl) putCursor(
	anchor, head, target term.Coordinates,
) (term.Coordinates, term.Coordinates) {
	switch {
	case !coordinatesBefore(head, anchor) && coordinatesBefore(target, anchor):
		if next, ok := h.nextPos(anchor); ok {
			anchor = next
		}
	case coordinatesBefore(head, anchor) && !coordinatesBefore(target, anchor):
		if prev, ok := h.prevPos(anchor); ok {
			anchor = prev
		}
	}
	if !coordinatesBefore(target, anchor) {
		next, ok := h.nextPos(target)
		if !ok {
			next = target
		}
		return anchor, next
	}
	return anchor, target
}

// wordMotion implements w/W, e/E and b/B. Normal mode installs the
// range word_move produced; select mode keeps the existing anchor and
// only adopts the new cursor, which is what extend_word_impl does.
func (h *helixHandlerImpl) wordMotion(target wordMotionTarget) bool {
	anchor, head := h.helixRange()
	newAnchor, newHead := h.wordMove(anchor, head, h.motionCount(), target)
	if newAnchor == anchor && newHead == head {
		return false
	}
	if h.extend {
		newAnchor, newHead = h.putCursor(
			anchor, head, h.rangeCursor(newAnchor, newHead))
	}
	h.applyHelixRange(newAnchor, newHead)
	return true
}

func (h *helixHandlerImpl) moveNextWordStart(group bool) bool {
	if group {
		return h.wordMotion(nextLongWordStart)
	}
	return h.wordMotion(nextWordStart)
}

func (h *helixHandlerImpl) moveNextWordEnd(group bool) bool {
	if group {
		return h.wordMotion(nextLongWordEnd)
	}
	return h.wordMotion(nextWordEnd)
}

func (h *helixHandlerImpl) movePrevWordStart(group bool) bool {
	if group {
		return h.wordMotion(prevLongWordStart)
	}
	return h.wordMotion(prevWordStart)
}

// findChar implements f/t/F/T. Helix offsets the search start so a
// till motion can be repeated without standing still, then selects from
// the original caret through the landing cell. A miss leaves the range
// untouched.
func (h *helixHandlerImpl) findChar(mode moveMode, ch rune) bool {
	count := h.motionCount()
	return h.runMotionOnce(func() bool {
		origin := h.cursor.CursorAtScroll()
		prevAnchor, hadAnchor := h.selectionAnchor()
		h.cursor.Unselect()
		h.explicitSel = false
		findNext := func() bool {
			return h.moved(func() bool { return h.cursor.MoveToNextChar(ch) })
		}
		findPrev := func() bool {
			return h.moved(func() bool { return h.cursor.MoveToPrevChar(ch) })
		}
		nth := func(step func() bool) bool {
			for range count {
				if !step() {
					return false
				}
			}
			return true
		}
		var ok bool
		switch mode {
		case moveToNext:
			ok = nth(findNext)
		case moveToPrev:
			ok = nth(findPrev)
		case moveTillNext:
			// The search starts one cell further on so a repeat cannot
			// land back on the character the caret already sits next to.
			if h.cursor.MoveRight() {
				if ok = nth(findNext); ok {
					h.cursor.MoveLeft()
				}
			}
		case moveTillPrev:
			if h.cursor.MoveLeft() {
				if ok = nth(findPrev); ok {
					h.cursor.MoveRight()
				}
			}
		}
		if !ok || h.cursor.CursorAtScroll() == origin {
			h.cursor.MoveToScroll(origin)
			if hadAnchor {
				h.setSelectionRange(prevAnchor, origin)
			} else {
				h.setSelectionRange(origin, origin)
			}
			return false
		}
		h.setSelectionRange(origin, h.cursor.CursorAtScroll())
		return true
	})
}

// gotoLineEnd puts the caret on the last cell of the line rather than
// past it, which is where Helix's goto_line_end lands.
func (h *helixHandlerImpl) gotoLineEnd() bool {
	if !h.cursor.MoveEndLine() {
		return false
	}
	pos := h.cursor.CursorAtScroll()
	if pos.X > 0 {
		h.cursor.MoveLeft()
	}
	return true
}

// gotoColumn implements g| : the count-th column of the current line,
// clamped to the line end.
func (h *helixHandlerImpl) gotoColumn() bool {
	pos := h.cursor.CursorAtScroll()
	if pos.Y >= h.less.Buffer().Rows() {
		return false
	}
	pos.X = max(0, min(h.motionCount()-1, h.less.Buffer().Columns(pos.Y)-1))
	_, ok := h.cursor.MoveToScroll(pos)
	return ok
}

// gotoLine implements G and a counted gg: jump to the count-th line,
// skipping a trailing blank last line the way Helix does.
func (h *helixHandlerImpl) gotoLine() bool {
	buf := h.less.Buffer()
	rows := buf.Rows()
	if rows == 0 {
		return false
	}
	maxLine := rows - 1
	if buf.Columns(maxLine) == 0 && rows > 1 {
		maxLine--
	}
	target := max(0, min(h.motionCount()-1, maxLine))
	_, ok := h.cursor.MoveToScroll(term.Coordinates{Y: target})
	return ok
}

// gotoLastLine skips a trailing blank last line, matching
// goto_last_line_impl.
func (h *helixHandlerImpl) gotoLastLine() bool {
	buf := h.less.Buffer()
	rows := buf.Rows()
	if rows == 0 {
		return false
	}
	target := rows - 1
	if buf.Columns(target) == 0 && rows > 1 {
		target--
	}
	_, ok := h.cursor.MoveToScroll(term.Coordinates{Y: target})
	return ok
}

// moveToMatch implements n/N: the caret jumps to the match and the
// selection covers it, as search_impl does. The scan starts from the
// far edge of the current selection so a match already under the caret
// is not found again.
func (h *helixHandlerImpl) moveToMatch(dir moveMode) bool {
	return h.jumping(func() bool {
		return h.runMotion(func() bool {
			from, to, hadSelection := h.cursor.SelectionRange()
			h.cursor.Unselect()
			h.explicitSel = false
			if hadSelection {
				if dir == moveToPrev {
					h.cursor.MoveToScroll(from)
				} else {
					last := to
					if last.X > 0 {
						last.X--
					}
					h.cursor.MoveToScroll(last)
				}
			}
			var ok bool
			if dir == moveToPrev {
				ok = h.cursor.MoveToPrevMatch()
			} else {
				ok = h.cursor.MoveToNextMatch()
			}
			if !ok {
				h.anchorHere()
				return false
			}
			h.selectMatchAtCaret()
			return true
		})
	})
}

// selectMatchAtCaret covers the search hit the caret was just moved to.
func (h *helixHandlerImpl) selectMatchAtCaret() {
	width := len([]rune(h.searchText))
	h.cursor.Select()
	h.explicitSel = false
	for range max(0, width-1) {
		if !h.cursor.MoveRightWrap() {
			break
		}
	}
}

// scrollLine scrolls the viewport and only drags the caret along when it
// would otherwise leave the window.
func (h *helixHandlerImpl) scrollLine(dir int) bool {
	pos := h.cursor.CursorAtScroll()
	scroll := h.less.Scroll()
	moved := false
	for range h.motionCount() {
		if dir > 0 {
			moved = scroll.SeekDown() || moved
		} else {
			moved = scroll.SeekUp() || moved
		}
	}
	if !moved {
		return false
	}
	win, _ := h.cursor.WindowCoordinates(pos)
	if win.Y >= 0 && win.Y < scroll.SizeHeight() {
		h.cursor.SetCursorAtScroll(pos)
	}
	return true
}

func (h *helixHandlerImpl) pageMotion(dir int) bool {
	return h.scrollBy(dir, max(1, h.less.Scroll().SizeHeight()-pagePadding))
}

func (h *helixHandlerImpl) halfPageMotion(dir int) bool {
	return h.scrollBy(dir, max(1, h.less.Scroll().SizeHeight()/2))
}

func (h *helixHandlerImpl) scrollBy(dir, by int) bool {
	return h.moveCaret(func() bool {
		if dir > 0 {
			if !h.cursor.MoveDownLines(by) {
				return false
			}
			h.cursor.RepositionTop()
			return true
		}
		if !h.cursor.MoveUpLines(by) {
			return false
		}
		h.cursor.RepositionBottom()
		return true
	})
}

// --- jumplist ---

type jumpEntry struct {
	anchor term.Coordinates
	head   term.Coordinates
}

const maxJumps = 64

// jumpIdx mirrors helix-view's JumpList::current: it points one past the
// newest entry after a push, so the first C-o records where the caret is
// now and then steps back onto the last pushed jump.
func (h *helixHandlerImpl) currentJump() jumpEntry {
	head := h.cursor.CursorAtScroll()
	anchor := head
	if a, ok := h.selectionAnchor(); ok {
		anchor = a
	}
	return jumpEntry{anchor: anchor, head: head}
}

func (h *helixHandlerImpl) pushJump() bool {
	h.pushJumpEntry(h.currentJump())
	return true
}

// jumping records where the caret was before an explicit jump so C-o
// can walk back to it, which is what Helix's push_jump does for G, g|,
// g., a confirmed search and n/N.
func (h *helixHandlerImpl) jumping(fn func() bool) bool {
	origin := h.currentJump()
	if !fn() {
		return false
	}
	h.pushJumpEntry(origin)
	return true
}

// pushJumpEntry reports how many entries were dropped off the front so a
// caller holding an index into the list can rebase it.
func (h *helixHandlerImpl) pushJumpEntry(entry jumpEntry) int {
	if h.jumpIdx >= 0 && h.jumpIdx < len(h.jumps) {
		h.jumps = h.jumps[:h.jumpIdx]
	}
	if n := len(h.jumps); n > 0 && h.jumps[n-1] == entry {
		return 0
	}
	removed := 0
	h.jumps = append(h.jumps, entry)
	if over := len(h.jumps) - maxJumps; over > 0 {
		h.jumps = h.jumps[over:]
		removed = over
	}
	h.jumpIdx = len(h.jumps)
	return removed
}

func (h *helixHandlerImpl) jumpBackward() bool {
	count := h.motionCount()
	if h.jumpIdx < count || len(h.jumps) == 0 {
		return false
	}
	next := h.jumpIdx - count
	if h.jumpIdx == len(h.jumps) {
		next -= h.pushJumpEntry(h.currentJump())
	}
	if next < 0 || next >= len(h.jumps) {
		return false
	}
	// Helix skips an entry that matches where the caret already is.
	if h.jumps[next] == h.currentJump() {
		if next == 0 {
			return false
		}
		next--
	}
	h.jumpIdx = next
	h.restoreJump(h.jumps[h.jumpIdx])
	return true
}

func (h *helixHandlerImpl) jumpForward() bool {
	next := h.jumpIdx + h.motionCount()
	if next >= len(h.jumps) {
		return false
	}
	h.jumpIdx = next
	h.restoreJump(h.jumps[h.jumpIdx])
	return true
}

func (h *helixHandlerImpl) restoreJump(entry jumpEntry) {
	h.setSelectionRange(entry.anchor, entry.head)
}
