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

// goto_word, from helix-term/src/commands.rs jump_to_word and
// jump_to_label. Every visible word of at least two word characters is
// given a two-character label drawn over its first two cells; typing a
// label selects that word.

// jumpAlphabet is editor.jump-label-alphabet's default, so a label
// addresses up to len(jumpAlphabet)^2 candidates.
var jumpAlphabet = []rune("abcdefghijklmnopqrstuvwxyz")

// jumpLabelAttr stands in for the ui.virtual.jump-label theme key.
// Labels replace buffer content, so they have to be unmistakably chrome.
var jumpLabelAttr = term.Attributes{
	Fg:    term.ColorBlack,
	Bg:    term.ColorFuchsia,
	Attrs: term.AttrBold,
}

// jumpRange is one candidate's (anchor, head) pair in document space.
type jumpRange struct {
	anchor term.Coordinates
	head   term.Coordinates
}

func (r jumpRange) backward() bool { return coordinatesBefore(r.head, r.anchor) }

func (r jumpRange) from() term.Coordinates {
	if r.backward() {
		return r.head
	}
	return r.anchor
}

// forward is Range::with_direction(Direction::Forward).
func (r jumpRange) forward() jumpRange {
	if r.backward() {
		return jumpRange{anchor: r.head, head: r.anchor}
	}
	return r
}

func jumpAlphabetIndex(ch rune) int {
	for i, a := range jumpAlphabet {
		if a == ch {
			return i
		}
	}
	return -1
}

// jumpToWord labels the visible words and waits for the two label keys.
func (h *helixHandlerImpl) jumpToWord(extend bool) bool {
	labels := h.jumpCandidates()
	if len(labels) == 0 {
		return false
	}
	h.jumpLabels = labels
	h.jumpExtend = extend
	h.jumpOuter = -1
	h.setMode(jumpMode)
	return true
}

// jumpCandidates walks outward from the caret, alternating forwards and
// backwards, so the labels closest to the caret come first. The word the
// caret is already on is skipped: the walk starts from its own edges.
func (h *helixHandlerImpl) jumpCandidates() []jumpRange {
	limit := len(jumpAlphabet) * len(jumpAlphabet)
	start, end := h.visibleBounds()
	caret := h.cursor.CursorAtScroll()
	fwd := jumpRange{anchor: caret, head: caret}
	rev := fwd
	if ch, ok := h.charAt(caret); ok && !unicode.IsSpace(ch) {
		if a, hd := h.wordMove(caret, caret, 1, nextWordEnd); a == caret {
			fwd = jumpRange{anchor: a, head: hd}
		}
		after, ok := h.nextPos(caret)
		if a, hd := h.wordMove(caret, caret, 1, prevWordStart); ok && a == after {
			rev = jumpRange{anchor: a, head: hd}
		}
	}

	words := make([]jumpRange, 0, limit)
	for len(words) < limit {
		changed := false
		for coordinatesBefore(fwd.head, end) {
			next := h.step(fwd, nextWordEnd)
			if next == fwd {
				break
			}
			fwd = next
			if !h.labelWorthy(fwd.head, true) {
				continue
			}
			changed = true
			fwd.anchor = h.skipToWord(fwd.anchor, true)
			words = append(words, fwd)
			break
		}
		if len(words) == limit {
			break
		}
		for coordinatesBefore(start, rev.head) {
			next := h.step(rev, prevWordStart)
			if next == rev {
				break
			}
			rev = next
			if !h.labelWorthy(rev.head, false) {
				continue
			}
			changed = true
			rev.anchor = h.skipToWord(rev.anchor, false)
			words = append(words, rev)
			break
		}
		if !changed {
			break
		}
	}
	return words
}

func (h *helixHandlerImpl) step(r jumpRange, target wordMotionTarget) jumpRange {
	anchor, head := h.wordMove(r.anchor, r.head, 1, target)
	return jumpRange{anchor: anchor, head: head}
}

// visibleBounds is the half-open document range labels are offered over:
// the start of the first visible row up to the start of the row after
// the last visible one.
func (h *helixHandlerImpl) visibleBounds() (start, end term.Coordinates) {
	scroll := h.less.Scroll()
	top := scroll.WindowToScrollCoordinates(term.Coordinates{})
	bottom := scroll.WindowToScrollCoordinates(term.Coordinates{Y: scroll.SizeHeight()})
	start = term.Coordinates{Y: max(0, top.Y)}
	end = term.Coordinates{Y: min(h.less.Buffer().Rows(), max(start.Y, bottom.Y))}
	return start, end
}

// labelWorthy is jump_to_word's add_label test. A word motion stops on
// any run of same-category cells, so without it operator soup like "=<"
// would be offered a label too.
func (h *helixHandlerImpl) labelWorthy(head term.Coordinates, forward bool) bool {
	// A forward range ends one past the word; a backward one starts on
	// its first cell.
	if !forward {
		next, ok := h.nextPos(head)
		return ok && h.isWordCell(head) && h.isWordCell(next)
	}
	last, ok := h.prevPos(head)
	if !ok {
		return false
	}
	prior, ok := h.prevPos(last)
	return ok && h.isWordCell(last) && h.isWordCell(prior)
}

func (h *helixHandlerImpl) isWordCell(pos term.Coordinates) bool {
	ch, ok := h.charAt(pos)
	return ok && categorizeChar(ch) == catWord
}

// skipToWord trims the whitespace a word motion swept up so the label
// lands on the word itself.
func (h *helixHandlerImpl) skipToWord(pos term.Coordinates, forward bool) term.Coordinates {
	for {
		probe, ok := pos, true
		if !forward {
			probe, ok = h.prevPos(pos)
		}
		if !ok || h.isWordCell(probe) {
			return pos
		}
		next := probe
		if forward {
			if next, ok = h.nextPos(pos); !ok {
				return pos
			}
		}
		pos = next
	}
}

// handleJump consumes the two label keys. Anything else cancels, which
// is jump_to_label's on_next_key contract.
func (h *helixHandlerImpl) handleJump(ev term.Event) (quit, handled bool) {
	if ev.Type != term.EventKey {
		return false, true
	}
	idx := -1
	if ev.Mod == 0 {
		idx = jumpAlphabetIndex(ev.Ch)
	}
	if idx < 0 {
		h.cancelJump()
		return false, true
	}
	if h.jumpOuter < 0 {
		outer := idx * len(jumpAlphabet)
		if outer > len(h.jumpLabels) {
			h.cancelJump()
			return false, true
		}
		h.jumpOuter = outer
		return false, true
	}

	labels, at, extend := h.jumpLabels, h.jumpOuter+idx, h.jumpExtend
	h.cancelJump()
	if at < len(labels) {
		h.applyJump(labels[at], extend)
	}
	return false, true
}

func (h *helixHandlerImpl) clearJumpLabels() {
	h.jumpLabels = nil
	h.jumpOuter = -1
	h.jumpExtend = false
}

func (h *helixHandlerImpl) cancelJump() {
	h.clearJumpLabels()
	h.setMode(normalMode)
}

func (h *helixHandlerImpl) applyJump(target jumpRange, extend bool) {
	rng := target.forward()
	if extend {
		rng = jumpRange{anchor: h.extendedJumpAnchor(target), head: target.head}
	}
	h.pushJump()
	h.applyHelixRange(rng.anchor, rng.head)
}

// extendedJumpAnchor keeps whichever end of the live selection is
// further from the label, so extend_to_word grows the range instead of
// replacing it.
func (h *helixHandlerImpl) extendedJumpAnchor(target jumpRange) term.Coordinates {
	from, to, ok := h.cursor.SelectionRange()
	if !ok {
		return target.anchor
	}
	if target.backward() {
		to = h.docPos(to)
		if coordinatesBefore(to, target.anchor) {
			return target.anchor
		}
		return to
	}
	if coordinatesBefore(target.anchor, from) {
		return target.anchor
	}
	return from
}

func (h *helixHandlerImpl) drawJumpLabels(w term.Writer) {
	for i, label := range h.jumpLabels {
		first := label.from()
		second, ok := h.nextPos(first)
		if !ok {
			continue
		}
		h.drawJumpLabel(w, first, jumpAlphabet[i/len(jumpAlphabet)])
		h.drawJumpLabel(w, second, jumpAlphabet[i%len(jumpAlphabet)])
	}
}

func (h *helixHandlerImpl) drawJumpLabel(w term.Writer, pos term.Coordinates, ch rune) {
	scroll := h.less.Scroll()
	at, ok := scroll.ScrollToWindowCoordinates(pos)
	if !ok || at.X < 0 || at.X >= scroll.Width() {
		return
	}
	w.SetCell(at, term.NewCell(ch, 1, jumpLabelAttr))
}
