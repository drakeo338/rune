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
	"context"
	"fmt"
	"strconv"
	"strings"

	log "github.com/sirupsen/logrus"
	"github.com/unstablebuild/blue/logging"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/clipboard"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"
	"unstable.build/rune/internal/cell"
	"unstable.build/rune/internal/component"
	"unstable.build/rune/internal/handler"
	"unstable.build/rune/internal/text"
)

type helixMode uint8
type moveMode uint8
type surroundOp uint8

const (
	normalMode helixMode = iota
	insertMode
	gotoMode
	matchMode
	viewMode
	replaceMode
	bracketMode
	jumpMode
	searchMode // never stored in currMode, only reported by mode()
)

const (
	moveNone moveMode = iota
	moveToNext
	moveToPrev
	moveTillNext
	moveTillPrev
)

const (
	surroundNone surroundOp = iota
	surroundAdd
	surroundReplace
	surroundDelete
)

const (
	lastChangeLocationListID = "changes"
	matchingLocID            = "_matchingMark"
)

const pagePadding = 2

var _ helixHandler = (*helixHandlerImpl)(nil)

type helixHandler interface {
	tui.Handler

	mode() helixMode
	extending() bool
	moveToNextLocation(ID string) bool
	moveToPrevLocation(ID string) bool
	setLocationList(pri textapi.LocationPriority, ID string, l text.LocationList)
	setCursorAtScroll(pos term.Coordinates) bool
	setNormalMode() bool
	cursorAtScroll() term.Coordinates
	search(string)
	moveToBounds()
	unselect() bool
	copySuppressed() bool
	setStatusBar(bar statusBar)
	insertBeforeSelection()
	takeCount() int
	takeUndoCheckpoint() bool
}

// helixHandlerImpl implements Helix's inverted modal grammar: a motion
// creates or extends the single selection owned by text.Cursor and an
// operator then acts on that selection. Multiple selections are out of
// scope; Helix's selection-set commands are deliberately left unbound.
type helixHandlerImpl struct {
	config    helixConfig
	less      handler.Less
	statusBar statusBar
	anchor    term.Coordinates
	cursor    text.Cursor
	currMode  helixMode

	// extend is Helix's sticky select mode (v): motions grow the
	// selection instead of replacing it.
	extend bool

	// explicitSel tracks selections built by SelectRange-style calls
	// (text objects, select-all, syntax expand). Those are pinned at
	// both ends, so a motion cannot extend them until they are turned
	// back into an anchor/head pair.
	explicitSel bool

	// stickyView keeps Z's view mode active until <esc>.
	stickyView bool

	moveMode     moveMode
	lastMoveMode moveMode
	moveChar     rune
	searchMode   moveMode
	searchText   string

	matchPending   bool
	matchAround    bool
	surroundMode   surroundOp
	surroundFrom   rune
	bracketForward bool

	pendingRegister  bool
	selectedRegister rune

	countDigits string
	count       int

	insertRegister        strings.Builder
	pendingInsertRegister bool
	undoCheckpoint        bool

	// insertKeepsCaret marks the one insert entry (i) that leaves a
	// non-empty range behind with its head at the start. Escaping such
	// a range leaves the caret on the cell it was already on instead of
	// pulling it back onto the text just typed.
	insertKeepsCaret bool

	jumps   []jumpEntry
	jumpIdx int

	// jumpLabels holds goto_word's live candidates while the two label
	// keys are pending; jumpOuter is -1 until the first one lands.
	jumpLabels []jumpRange
	jumpOuter  int
	jumpExtend bool

	pendingSetCursor *term.Coordinates
	setLocations     bool

	pasteBuf     strings.Builder
	pasteStarted bool

	suppressCopyDelete bool
}

type statusBar interface {
	SetStatus(string, term.Attributes)
}

func (h *helixHandlerImpl) init(buf *cell.Buffer, cfg helixConfig) {
	h.config = cfg
	h.statusBar = nopBar{}
	h.less.InitWithBuffer(buf, handler.LessConfig{
		Wrap:               h.config.wrap,
		ResAttr:            h.config.resAttr,
		SuperimposeMessage: true,
		BarAttr:            h.config.barAttr,
		MessageLayout:      h.config.messageBarLayout,
		Attributes:         h.config.attr,
	})
	scroll := h.less.Scroll()
	scroll.SetTabspaces(h.config.tabspaces)
	scroll.Attributes = h.config.attr
	scroll.ResultsAttr = h.config.resAttr
	scroll.Subscribe(h)
	h.cursor.Init(scroll, h.config.scheduleNextTick)
	h.cursor.RightInclusiveSemantics = true

	h.jumpIdx = -1
	h.anchor = h.cursorAtScroll()
	h.setMode(normalMode)
	h.jumpOuter = -1
	h.resetCount()
	// Helix always owns a selection, at minimum the cell under the
	// caret, so every operator has something to act on.
	h.anchorHere()
}

func (h *helixHandlerImpl) initWithScroll(scroll *component.Scroll, opts ...Option) {
	h.config = defaultHelixConfig()
	for _, o := range opts {
		o(&h.config)
	}
	h.statusBar = nopBar{}
	h.less.InitWithScroll(scroll, handler.LessConfig{
		Wrap:               h.config.wrap,
		ResAttr:            h.config.resAttr,
		SuperimposeMessage: true,
		BarAttr:            h.config.barAttr,
		MessageLayout:      h.config.messageBarLayout,
		Attributes:         h.config.attr,
	})
	h.less.Scroll().SetTabspaces(h.config.tabspaces)
	h.cursor.InitPerformance(h.less.Scroll())
	h.cursor.RightInclusiveSemantics = true
	h.jumpIdx = -1
	h.anchor = h.cursorAtScroll()
	h.setMode(normalMode)
	h.jumpOuter = -1
	h.resetCount()
	h.anchorHere()
}

func (h *helixHandlerImpl) setStatusBar(bar statusBar) {
	h.statusBar = bar
	h.setMode(h.currMode)
}

// Resize satisfies tui.Component.
func (h *helixHandlerImpl) Resize(width, height int) {
	h.less.Resize(width, height)
	if h.pendingSetCursor != nil {
		pos := *h.pendingSetCursor
		h.setCursorAtScroll(pos)
		h.pendingSetCursor = nil
	}
	// Selecting before the scroll has a size fails, so the invariant
	// that normal mode always owns a selection is restored here.
	if _, ok := h.cursor.SelectionMode(); !ok && h.currMode == normalMode {
		h.anchorHere()
	}
}

func (h *helixHandlerImpl) setActiveLocationListMessage(locs []textapi.Location) {
	for _, loc := range locs {
		if loc.Message != "" {
			h.less.SetMessage("%s", loc.Message)
			return
		}
	}
	h.less.SetMessage("")
}

func (h *helixHandlerImpl) drawLocationMessage() {
	locs, ok := h.cursor.LocationsAtCursor()
	if ok {
		h.setActiveLocationListMessage(locs)
		h.setLocations = true
	} else if h.setLocations {
		h.less.SetMessage("")
		h.setLocations = false
	}
}

// Draw satisfies tui.Component.
func (h *helixHandlerImpl) Draw(w term.Writer) {
	h.drawLocationMessage()
	h.less.Draw(w)
	text.DrawLocations(h.cursor.SortedLocations(), h.less.Scroll(), w)
	h.drawJumpLabels(w)
}

// Cursor satisfies tui.Handler.
func (h *helixHandlerImpl) Cursor() (term.Coordinates, term.CursorStyle, bool) {
	var style term.CursorStyle
	switch h.mode() {
	case searchMode:
		return h.less.Cursor()
	case insertMode:
		style = term.CursorStyleSteadyBar
	case gotoMode, matchMode, viewMode, replaceMode, bracketMode, jumpMode:
		style = term.CursorStyleSteadyUnderline
	case normalMode:
		style = term.CursorStyleDefault
	default:
		panic(fmt.Sprintf("unknown mode: %d", h.currMode))
	}
	return h.cursor.Coordinates(), style, true
}

func (h *helixHandlerImpl) setMode(mode helixMode) {
	var label string
	var attrs term.Attributes
	switch mode {
	case normalMode:
		if h.extend {
			label = " SELECT"
			attrs = term.Attributes{Bg: term.ColorBlue, Fg: term.ColorBlack}
		} else {
			label = " NORMAL"
		}
	case insertMode:
		label = " INSERT"
		attrs = term.Attributes{Bg: term.ColorGreen, Fg: term.ColorBlack}
	case gotoMode:
		label = "  GOTO "
		attrs = term.Attributes{Bg: term.ColorFuchsia, Fg: term.ColorBlack}
	case matchMode:
		label = " MATCH "
		attrs = term.Attributes{Bg: term.ColorLime, Fg: term.ColorBlack}
	case viewMode:
		label = "  VIEW "
		attrs = term.Attributes{Bg: term.GetColor("orange"), Fg: term.ColorBlack}
	case replaceMode:
		label = "REPLACE"
		attrs = term.Attributes{Bg: term.ColorYellow, Fg: term.ColorBlack}
	case bracketMode:
		label = "BRACKET"
		attrs = term.Attributes{Bg: term.ColorAqua, Fg: term.ColorBlack}
	case jumpMode:
		label = "  JUMP "
		attrs = term.Attributes{Bg: term.ColorFuchsia, Fg: term.ColorBlack}
	case searchMode:
		label = " SEARCH"
		attrs = term.Attributes{Bg: term.ColorSilver, Fg: term.ColorBlack}
	default:
		panic(fmt.Sprintf("unknown mode: %v", mode))
	}
	h.statusBar.SetStatus(label, attrs)
	h.currMode = mode
}

func (h *helixHandlerImpl) setNormalMode() bool {
	if h.less.Mode() == handler.LessSearchMode {
		h.less.SetNormalMode()
	}
	h.resetCount()
	h.extend = false
	h.stickyView = false
	h.moveMode = moveNone
	h.matchPending = false
	h.surroundMode = surroundNone
	h.pendingRegister = false
	h.pendingInsertRegister = false
	h.clearJumpLabels()
	h.setMode(normalMode)
	return true
}

// exitSelectMode mirrors Helix's exit_select_mode: operators drop the
// sticky extend flag but leave everything else alone.
func (h *helixHandlerImpl) exitSelectMode() {
	if !h.extend {
		return
	}
	h.extend = false
	h.setMode(normalMode)
}

func (h *helixHandlerImpl) setInsertMode() {
	h.insertRegister.Reset()
	h.moveMode = moveNone
	h.matchPending = false
	h.surroundMode = surroundNone
	h.extend = false
	h.insertKeepsCaret = false
	h.setMode(insertMode)
	h.less.SetMessage("")
	h.resetCount()
}

func (h *helixHandlerImpl) mode() helixMode {
	if h.currMode == normalMode && h.less.Mode() != handler.LessNormalMode {
		return searchMode
	}
	return h.currMode
}

func (h *helixHandlerImpl) extending() bool { return h.extend }

func (h *helixHandlerImpl) unselect() bool { return h.cursor.Unselect() }

func (h *helixHandlerImpl) copySuppressed() bool { return h.suppressCopyDelete }

func (h *helixHandlerImpl) takeUndoCheckpoint() bool {
	taken := h.undoCheckpoint
	h.undoCheckpoint = false
	return taken
}

func (h *helixHandlerImpl) logError(err error) {
	log.WithField(logging.KeyClass, "helix.handler").Error(err)
}

func (h *helixHandlerImpl) selectionText() string { return h.cursor.Selection() }

// --- counts ---

func (h *helixHandlerImpl) resetCount() {
	h.countDigits = ""
	h.count = 1
}

func (h *helixHandlerImpl) parseCountDigit(ch rune) bool {
	if ch < '0' || ch > '9' {
		return false
	}
	if ch == '0' && h.countDigits == "" {
		return false
	}
	h.countDigits += string(ch)
	count, err := strconv.Atoi(h.countDigits)
	if err != nil {
		h.resetCount()
		return false
	}
	h.count = count
	return true
}

func (h *helixHandlerImpl) motionCount() int { return max(1, h.count) }

func (h *helixHandlerImpl) hasCount() bool { return h.countDigits != "" }

// takeCount hands the pending count to the wrapper, which owns the
// buffer-level undo commands.
func (h *helixHandlerImpl) takeCount() int {
	count := h.motionCount()
	h.resetCount()
	return count
}

// --- registers ---

func (h *helixHandlerImpl) activeRegister() rune {
	if h.selectedRegister != 0 {
		return h.selectedRegister
	}
	return unnamedRegister
}

func (h *helixHandlerImpl) consumeActiveRegister() rune {
	name := h.activeRegister()
	h.selectedRegister = 0
	return name
}

func (h *helixHandlerImpl) readRegister(name rune) (clipboard.Data, error) {
	return h.config.clipboard.Paste(registerNameToID(name))
}

func (h *helixHandlerImpl) writeRegister(name rune, data clipboard.Data) error {
	return h.config.clipboard.Copy(registerNameToID(name), data)
}

func (h *helixHandlerImpl) macroPlaybackActive() bool {
	return h.config.macroPlayer != nil && h.config.macroPlayer.IsPlaying()
}

// --- search ---

func (h *helixHandlerImpl) search(target string) {
	if h.config.disableSearch {
		return
	}
	h.searchText = target
	h.cursor.Search(target)
}

// searchSelection implements * and A-*: the selected text becomes the
// search pattern, optionally anchored at word boundaries.
func (h *helixHandlerImpl) searchSelection(detectWordBoundaries bool) bool {
	target := h.selectionText()
	if target == "" || h.config.disableSearch {
		return false
	}
	h.searchMode = moveToNext
	h.searchText = target
	if detectWordBoundaries && h.cursor.Word() == target {
		h.cursor.SearchWord(target)
	} else {
		h.cursor.Search(target)
	}
	if err := h.writeRegister('/', clipboard.Data{Text: target}); err != nil {
		h.logError(err)
	}
	return true
}

func (h *helixHandlerImpl) handleSearch(ev term.Event) (bool, bool) {
	if ev.Mod == 0 && ev.Key == term.KeyEnter {
		target := h.less.SearchText()
		h.less.SetNormalMode()
		if target == "" {
			h.less.SetMessage("")
		} else {
			h.less.SetMessage("searching '%s'", target)
		}
		h.search(target)
		if err := h.writeRegister('/', clipboard.Data{Text: target}); err != nil {
			h.logError(err)
		}
		// Confirming the prompt jumps to and selects the first match,
		// which is what search_impl does on Enter.
		h.moveToMatch(h.searchMode)
		return false, true
	}
	return h.less.Handle(ev)
}

func (h *helixHandlerImpl) beginSearch(ev term.Event, dir moveMode) bool {
	h.searchMode = dir
	if h.config.disableSearch {
		return false
	}
	_, handled := h.less.Handle(ev)
	return handled
}

func reverseDirection(mode moveMode) moveMode {
	switch mode {
	case moveToNext:
		return moveToPrev
	case moveToPrev:
		return moveToNext
	case moveTillNext:
		return moveTillPrev
	case moveTillPrev:
		return moveTillNext
	default:
		return moveNone
	}
}

// --- matching brace highlight ---

func (h *helixHandlerImpl) markMatchingBrace() {
	h.cursor.SetLocationList(textapi.LocationPriorityInfo, matchingLocID, nil)
	c, ok := h.cursor.Cell()
	if !ok {
		return
	}
	var matching rune
	var end bool
	switch c.Ch {
	case '{':
		matching = '}'
	case '(':
		matching = ')'
	case '[':
		matching = ']'
	case '}':
		matching, end = '{', true
	case ')':
		matching, end = '(', true
	case ']':
		matching, end = '[', true
	default:
		return
	}

	var pos term.Coordinates
	if end {
		pos, ok = h.cursor.FindMatchingRuneBackward(c.Ch, matching)
	} else {
		pos, ok = h.cursor.FindMatchingRuneForward(c.Ch, matching)
	}
	if !ok {
		return
	}
	to := pos
	to.X++
	h.cursor.SetLocationList(textapi.LocationPriorityInfo, matchingLocID,
		textapi.LocationSlice([]textapi.Location{
			{From: pos, To: to, Attr: term.Attributes{Attrs: term.AttrReverse}},
		}))
}

// --- dispatch ---

// Handle satisfies tui.Handler.
func (h *helixHandlerImpl) Handle(ev term.Event) (quit, handled bool) {
	// Only a user event clears a pending set cursor.
	h.pendingSetCursor = nil

	switch ev.Type {
	case term.EventPasteStart:
		h.pasteBuf.Reset()
		h.pasteStarted = true
		return false, true
	case term.EventPasteEnd:
		str := h.pasteBuf.String()
		h.pasteStarted = false
		if h.mode() == insertMode && str != "" {
			h.cursor.InsertString(str)
			h.insertRegister.WriteString(str)
		}
		return false, true
	}

	if h.pasteStarted {
		if ev.Ch != 0 {
			h.pasteBuf.WriteRune(ev.Ch)
		}
		return false, true
	}

	mode := h.mode()
	defer h.doneHandle(mode)

	switch mode {
	case searchMode:
		return h.handleSearch(ev)
	case normalMode:
		return h.handleNormal(ev)
	case insertMode:
		return h.handleInsert(ev)
	case gotoMode:
		return h.handleGoto(ev)
	case matchMode:
		return h.handleMatch(ev)
	case viewMode:
		return h.handleView(ev)
	case replaceMode:
		return h.handleReplace(ev)
	case bracketMode:
		return h.handleBracket(ev)
	case jumpMode:
		return h.handleJump(ev)
	default:
		panic(fmt.Sprintf("unknown mode: %d", h.currMode))
	}
}

func (h *helixHandlerImpl) doneHandle(mode helixMode) {
	h.doMoveToBounds()
	if mode == normalMode {
		h.markMatchingBrace()
	}
}

func (h *helixHandlerImpl) moveToBounds() {
	h.doMoveToBounds()
	h.markMatchingBrace()
}

func (h *helixHandlerImpl) doMoveToBounds() {
	if h.cursor.CursorAtScroll().Y < h.less.Buffer().Rows() {
		h.anchor = h.cursorAtScroll()
	}
	if !h.config.cursorCorrections {
		return
	}
	if h.mode() == insertMode {
		h.cursor.MoveToBounds(1)
		return
	}
	prev := h.cursor.Coordinates()
	h.cursor.MoveToBounds(0)
	if h.cursor.Coordinates().Y != prev.Y {
		h.anchor = h.cursorAtScroll()
	}
}

// handleNormal dispatches both normal and select mode; the difference
// between them is the sticky extend flag, not the key table.
func (h *helixHandlerImpl) handleNormal(ev term.Event) (quit, handled bool) {
	doResetCount := true
	defer func() {
		if doResetCount {
			h.resetCount()
		}
	}()

	if h.moveMode != moveNone {
		return h.handleFindChar(ev)
	}

	if h.pendingRegister {
		h.pendingRegister = false
		if ev.Ch == 0 && ev.Key == 0 {
			return false, true
		}
		if ev.Mod == 0 && validRegisterName(ev.Ch) {
			h.selectedRegister = normalizedRegisterName(ev.Ch)
			doResetCount = false
			return false, true
		}
		h.selectedRegister = 0
		return false, true
	}

	switch ev.Mod {
	case term.ModCtrl:
		handled = true
		switch ev.Ch {
		case 'b':
			handled = h.pageMotion(-1)
		case 'f':
			handled = h.pageMotion(1)
		case 'u':
			handled = h.halfPageMotion(-1)
		case 'd':
			handled = h.halfPageMotion(1)
		case 'e':
			handled = h.scrollLine(1)
		case 'y':
			handled = h.scrollLine(-1)
		case 'c':
			handled = h.toggleComments()
			h.exitSelectMode()
		case 'a':
			handled = h.increment(h.motionCount())
		case 'x':
			handled = h.increment(-h.motionCount())
		case 's':
			handled = h.pushJump()
		case 'o':
			handled = h.jumpBackward()
		case 'i':
			handled = h.jumpForward()
		default:
			handled = false
		}
	case term.ModAlt:
		handled = true
		switch ev.Key {
		case term.KeyArrowUp:
			handled = h.expandSelection()
		case term.KeyArrowDown:
			handled = h.shrinkSelection()
		default:
			switch ev.Ch {
			case 'd':
				handled = h.deleteSelection(false)
				h.anchorHere()
				h.exitSelectMode()
			case 'c':
				h.changeSelection(false)
			case ';':
				handled = h.cursor.SwapSelectionEnd()
			case ':':
				handled = h.ensureSelectionForward()
			case 'x':
				handled = h.shrinkToLineBounds()
			case 'o':
				handled = h.expandSelection()
			case 'i':
				handled = h.shrinkSelection()
			case '`':
				handled = h.keepSelection(h.cursor.UppercaseSelection)
				h.exitSelectMode()
			case '.':
				handled = h.repeatLastMotion()
			case 'J':
				handled = h.joinSelection(true)
			case '*':
				handled = h.searchSelection(false)
			default:
				handled = false
			}
		}
	case 0:
		handled = true
		switch ev.Ch {
		case '"':
			h.pendingRegister = true
			doResetCount = false
		case 'h':
			handled = h.moveCaret(h.cursor.MoveLeft)
		case 'l':
			handled = h.moveCaret(h.cursor.MoveRight)
		case 'j':
			handled = h.moveVertically(1)
		case 'k':
			handled = h.moveVertically(-1)
		case 'w':
			handled = h.moveNextWordStart(false)
		case 'W':
			handled = h.moveNextWordStart(true)
		case 'e':
			handled = h.moveNextWordEnd(false)
		case 'E':
			handled = h.moveNextWordEnd(true)
		case 'b':
			handled = h.movePrevWordStart(false)
		case 'B':
			handled = h.movePrevWordStart(true)
		case 'f':
			h.beginFindChar(moveToNext)
			doResetCount = false
		case 'F':
			h.beginFindChar(moveToPrev)
			doResetCount = false
		case 't':
			h.beginFindChar(moveTillNext)
			doResetCount = false
		case 'T':
			h.beginFindChar(moveTillPrev)
			doResetCount = false
		case 'G':
			// Bare G is a no-op in Helix: goto_line only acts on a count.
			handled = h.hasCount() && h.jumping(func() bool {
				return h.moveCaret(h.gotoLine)
			})
		case 'g':
			h.setMode(gotoMode)
			doResetCount = false
		case 'm':
			h.setMode(matchMode)
			doResetCount = false
		case 'z':
			h.setMode(viewMode)
			doResetCount = false
		case 'Z':
			h.stickyView = true
			h.setMode(viewMode)
			doResetCount = false
		case '[':
			h.bracketForward = false
			h.setMode(bracketMode)
			doResetCount = false
		case ']':
			h.bracketForward = true
			h.setMode(bracketMode)
			doResetCount = false
		case 'v':
			h.toggleExtend()
		case ';':
			h.anchorHere()
		case 'x':
			handled = h.extendLineBelow()
		case 'X':
			handled = h.extendToLineBounds()
		case '%':
			handled = h.selectAll()
		case '_':
			handled = h.trimSelection()
		case 'i':
			h.insertBeforeSelection()
		case 'a':
			h.insertAfterSelection()
		case 'I':
			h.insertAtLineStart()
		case 'A':
			h.insertAtLineEnd()
		case 'o':
			h.openLine(false)
		case 'O':
			h.openLine(true)
		case 'd':
			handled = h.deleteSelection(true)
			h.anchorHere()
			h.exitSelectMode()
		case 'c':
			h.changeSelection(true)
		case 'y':
			handled = h.yankSelection()
			h.exitSelectMode()
		case 'p':
			handled = h.pasteClipboard(true)
			h.exitSelectMode()
		case 'P':
			handled = h.pasteClipboard(false)
			h.exitSelectMode()
		case 'R':
			handled = h.replaceWithYanked()
			h.exitSelectMode()
		case 'r':
			h.setMode(replaceMode)
			doResetCount = false
		case '~':
			handled = h.keepSelection(h.cursor.ToggleCaseSelection)
			h.exitSelectMode()
		case '`':
			handled = h.keepSelection(h.cursor.LowercaseSelection)
			h.exitSelectMode()
		case 'J':
			handled = h.joinSelection(false)
		case '>':
			handled = h.shiftSelection(true)
			h.exitSelectMode()
		case '<':
			handled = h.shiftSelection(false)
			h.exitSelectMode()
		case '=':
			handled = h.formatSelection()
		case 'n':
			handled = h.moveToMatch(h.searchMode)
		case 'N':
			handled = h.moveToMatch(reverseDirection(h.searchMode))
		case '/':
			handled = h.beginSearch(ev, moveToNext)
		case '?':
			handled = h.beginSearch(ev, moveToPrev)
		case '*':
			handled = h.searchSelection(true)
		case 'Q':
			handled = h.toggleMacroRecording()
		case 'q':
			handled = h.playMacro()
		default:
			switch ev.Key {
			case term.KeyEsc:
				handled = h.setNormalMode()
			case term.KeyArrowLeft:
				handled = h.moveCaret(h.cursor.MoveLeft)
			case term.KeyArrowRight:
				handled = h.moveCaret(h.cursor.MoveRight)
			case term.KeyArrowDown:
				handled = h.moveVertically(1)
			case term.KeyArrowUp:
				handled = h.moveVertically(-1)
			case term.KeyHome:
				handled = h.moveCaret(h.cursor.MoveStartLine)
			case term.KeyEnd:
				handled = h.moveCaret(h.gotoLineEnd)
			case term.KeyPgup:
				handled = h.pageMotion(-1)
			case term.KeyPgdn:
				handled = h.pageMotion(1)
			case term.KeyTab:
				handled = h.jumpForward()
			default:
				if h.parseCountDigit(ev.Ch) {
					doResetCount = false
					return false, true
				}
				handled = false
			}
		}
	}
	return quit, handled
}

func (h *helixHandlerImpl) toggleExtend() {
	h.extend = !h.extend
	if h.extend {
		if _, ok := h.cursor.SelectionMode(); !ok {
			h.cursor.Select()
		}
	}
	h.setMode(normalMode)
}

// moveVertically keeps the desired column across short lines, which is
// what Helix's old_visual_position does.
func (h *helixHandlerImpl) moveVertically(dir int) bool {
	count := h.motionCount()
	return h.moveCaretOnce(func() bool {
		h.cursor.MoveToScroll(h.anchor)
		if dir > 0 {
			return h.cursor.MoveDownLines(count)
		}
		return h.cursor.MoveUpLines(count)
	})
}

func (h *helixHandlerImpl) expandSelection() bool {
	if !h.cursor.ExpandSelection(context.Background()) {
		return false
	}
	h.explicitSel = true
	return true
}

func (h *helixHandlerImpl) shrinkSelection() bool {
	if !h.cursor.ShrinkSelection() {
		return false
	}
	h.explicitSel = true
	return true
}

// ensureSelectionForward implements A-: so the head always trails the
// anchor, which makes the following operator direction-independent.
func (h *helixHandlerImpl) ensureSelectionForward() bool {
	if !h.selectionBackward() {
		return false
	}
	return h.cursor.SwapSelectionEnd()
}

func (h *helixHandlerImpl) toggleMacroRecording() bool {
	if h.config.macroRecorder == nil || h.macroPlaybackActive() {
		return false
	}
	if h.config.macroRecorder.IsRecording() {
		h.config.macroRecorder.Stop()
		return true
	}
	h.config.macroRecorder.Start(registerNameToID(h.consumeActiveRegister()))
	return true
}

func (h *helixHandlerImpl) playMacro() bool {
	if h.config.macroPlayer == nil {
		return false
	}
	reg := h.consumeActiveRegister()
	if err := h.config.macroPlayer.Play(registerNameToID(reg), h.motionCount()); err != nil {
		h.logError(fmt.Errorf("macro playback: %s", err))
	}
	return true
}

// --- find char ---

func (h *helixHandlerImpl) beginFindChar(mode moveMode) {
	h.moveMode = mode
	h.setMode(normalMode)
}

func (h *helixHandlerImpl) handleFindChar(ev term.Event) (quit, handled bool) {
	mode := h.moveMode
	h.moveMode = moveNone
	defer h.resetCount()
	if ev.Type != term.EventKey || ev.Mod != 0 || ev.Key == term.KeyEsc {
		return false, true
	}
	ch := ev.Ch
	switch ev.Key {
	case term.KeySpace:
		ch = ' '
	case term.KeyTab:
		ch = '\t'
	}
	if ch == 0 {
		return false, true
	}
	h.moveChar = ch
	h.lastMoveMode = mode
	h.findChar(mode, ch)
	return false, true
}

// repeatLastMotion implements A-. for the f/t/F/T family.
func (h *helixHandlerImpl) repeatLastMotion() bool {
	if h.lastMoveMode == moveNone || h.moveChar == 0 {
		return false
	}
	return h.findChar(h.lastMoveMode, h.moveChar)
}

// --- minor modes ---

func (h *helixHandlerImpl) handleGoto(ev term.Event) (quit, handled bool) {
	if ev.Type != term.EventKey {
		return false, true
	}
	defer func() {
		// goto_word takes over the mode to collect its label keys, so
		// only an ordinary goto command falls back to normal mode.
		if h.currMode == gotoMode {
			h.setMode(normalMode)
		}
		h.resetCount()
	}()

	if ev.Mod != 0 {
		return false, ev.Key == term.KeyEsc
	}
	if ev.Key == term.KeyEsc {
		return false, true
	}

	switch ev.Ch {
	case 'g':
		if h.hasCount() {
			return false, h.jumping(func() bool { return h.moveCaret(h.gotoLine) })
		}
		h.pushJump()
		return false, h.moveCaret(h.cursor.MoveFirstLine)
	case 'e':
		h.pushJump()
		return false, h.moveCaret(h.gotoLastLine)
	case 'h':
		return false, h.moveCaret(h.cursor.MoveStartLine)
	case 'l':
		return false, h.moveCaret(h.gotoLineEnd)
	case 's':
		return false, h.moveCaret(h.cursor.MoveStartLineNonBlank)
	case '|':
		return false, h.jumping(func() bool { return h.moveCaret(h.gotoColumn) })
	case 't':
		return false, h.moveCaret(h.cursor.MoveToWindowTop)
	case 'c':
		return false, h.moveCaret(h.cursor.MoveToWindowMiddle)
	case 'b':
		return false, h.moveCaret(h.cursor.MoveToWindowBottom)
	case 'k':
		return false, h.moveCaret(h.cursor.MoveLineUp)
	case 'j':
		return false, h.moveCaret(h.cursor.MoveLineDown)
	case '.':
		return false, h.jumping(func() bool {
			return h.moveCaret(func() bool {
				return h.cursor.MoveToNextLocation(lastChangeLocationListID)
			})
		})
	case 'w':
		return false, h.jumpToWord(h.extend)
	}
	return false, false
}

func (h *helixHandlerImpl) handleMatch(ev term.Event) (quit, handled bool) {
	if ev.Type != term.EventKey {
		return false, true
	}
	if ev.Key == term.KeyEsc || (ev.Mod != 0 && ev.Mod != term.ModShift) {
		h.matchPending = false
		h.surroundMode = surroundNone
		h.setMode(normalMode)
		h.resetCount()
		return false, ev.Key == term.KeyEsc
	}

	if h.surroundMode != surroundNone {
		return h.handleSurroundKey(ev.Ch)
	}

	if h.matchPending {
		// The key is consumed either way: an unresolved object just
		// leaves the selection alone.
		h.selectTextObject(ev.Ch)
		h.matchPending = false
		h.setMode(normalMode)
		h.resetCount()
		return false, true
	}

	switch ev.Ch {
	case 'i':
		h.matchPending = true
		h.matchAround = false
		return false, true
	case 'a':
		h.matchPending = true
		h.matchAround = true
		return false, true
	case 's':
		h.surroundMode = surroundAdd
		return false, true
	case 'r':
		h.surroundMode = surroundReplace
		h.surroundFrom = 0
		return false, true
	case 'd':
		h.surroundMode = surroundDelete
		return false, true
	case 'm':
		handled = h.moveCaret(h.cursor.MoveToMatchingRune)
	default:
		handled = false
	}
	h.setMode(normalMode)
	h.resetCount()
	return quit, handled
}

func (h *helixHandlerImpl) handleView(ev term.Event) (quit, handled bool) {
	if ev.Type != term.EventKey {
		return false, true
	}
	stay := h.stickyView
	defer func() {
		if !stay {
			h.stickyView = false
			h.setMode(normalMode)
		}
		h.resetCount()
	}()

	switch ev.Mod {
	case term.ModCtrl:
		switch ev.Ch {
		case 'f':
			return false, h.pageMotion(1)
		case 'b':
			return false, h.pageMotion(-1)
		case 'd':
			return false, h.halfPageMotion(1)
		case 'u':
			return false, h.halfPageMotion(-1)
		}
		return false, false
	case 0:
		switch ev.Key {
		case term.KeyEsc:
			stay = false
			return false, true
		case term.KeyArrowDown:
			return false, h.scrollLine(1)
		case term.KeyArrowUp:
			return false, h.scrollLine(-1)
		case term.KeyPgdn:
			return false, h.pageMotion(1)
		case term.KeyPgup:
			return false, h.pageMotion(-1)
		case term.KeySpace:
			return false, h.halfPageMotion(1)
		case term.KeyBackspace:
			return false, h.halfPageMotion(-1)
		}
		switch ev.Ch {
		case 'z', 'c':
			return false, h.cursor.Center()
		case 't':
			return false, h.cursor.RepositionTop()
		case 'b':
			return false, h.cursor.RepositionBottom()
		case 'm':
			return false, h.cursor.Center()
		case 'j':
			return false, h.scrollLine(1)
		case 'k':
			return false, h.scrollLine(-1)
		case 'n':
			return false, h.moveToMatch(h.searchMode)
		case 'N':
			return false, h.moveToMatch(reverseDirection(h.searchMode))
		case '/':
			stay = false
			return false, h.beginSearch(ev, moveToNext)
		case '?':
			stay = false
			return false, h.beginSearch(ev, moveToPrev)
		}
	}
	return false, false
}

// handleBracket dispatches the [ and ] minor modes. Helix fills these
// with diagnostic, change and syntax-object navigation; only the entries
// text.Cursor can serve are bound, the rest stay free for the command
// layer.
func (h *helixHandlerImpl) handleBracket(ev term.Event) (quit, handled bool) {
	if ev.Type != term.EventKey {
		return false, true
	}
	forward := h.bracketForward
	defer func() {
		h.setMode(normalMode)
		h.resetCount()
	}()
	if ev.Mod != 0 {
		return false, false
	}
	if ev.Key == term.KeyEsc {
		return false, true
	}
	if ev.Key == term.KeySpace {
		return false, h.addNewline(forward)
	}
	switch ev.Ch {
	case 'p':
		if forward {
			return false, h.moveCaret(func() bool {
				return h.cursor.MoveNextParagraphs(h.motionCount())
			})
		}
		return false, h.moveCaret(func() bool {
			return h.cursor.MovePrevParagraphs(h.motionCount())
		})
	}
	return false, false
}

func (h *helixHandlerImpl) handleReplace(ev term.Event) (quit, handled bool) {
	if ev.Type != term.EventKey {
		return false, true
	}
	defer func() {
		h.setMode(normalMode)
		h.resetCount()
	}()
	if ev.Mod != 0 || ev.Key == term.KeyEsc {
		return false, ev.Key == term.KeyEsc
	}
	ch := ev.Ch
	switch ev.Key {
	case term.KeySpace:
		ch = ' '
	case term.KeyTab:
		ch = '\t'
	case term.KeyEnter:
		ch = '\n'
	}
	if ch == 0 {
		return false, true
	}
	ok := h.replaceSelection(ch)
	h.exitSelectMode()
	return false, ok
}

// --- insert ---

func (h *helixHandlerImpl) exitInsert() {
	if inserted := h.insertRegister.String(); inserted != "" {
		if err := h.writeRegister('.', clipboard.Data{
			Text: inserted, Metadata: text.NoSelection,
		}); err != nil {
			h.logError(err)
		}
	}
	h.setNormalMode()
	if !h.insertKeepsCaret {
		h.cursor.MoveLeft()
	}
	h.insertKeepsCaret = false
	h.anchorHere()
}

func (h *helixHandlerImpl) insertTabIndent() {
	if h.cursor.TryIndent(h.config.indentRune, h.config.indentTabspaces) {
		h.insertRegister.WriteRune(h.config.indentRune)
		return
	}
	h.insertIndentLiteral()
}

func (h *helixHandlerImpl) insertIndentLiteral() {
	h.cursor.InsertWithIndentRune(
		h.config.indentRune, h.config.indentRune, h.config.indentTabspaces)
	h.insertRegister.WriteRune(h.config.indentRune)
}

// deleteToLineStart implements C-u: Helix kills back to the first
// non-blank, or to the line start when the caret is already there, or
// joins with the previous line at column zero.
func (h *helixHandlerImpl) deleteToLineStart() bool {
	pos := h.cursor.CursorAtScroll()
	if pos.X == 0 {
		if pos.Y == 0 {
			return false
		}
		return h.cursor.Backspace()
	}
	h.cursor.Unselect()
	h.explicitSel = false
	target := term.Coordinates{Y: pos.Y}
	if firstNonBlank, ok := h.firstNonBlank(pos.Y); ok && firstNonBlank.X < pos.X {
		target = firstNonBlank
	}
	// An explicit range keeps the delete right-exclusive, so the cell
	// the caret sits on survives.
	if !h.cursor.SelectRange(target, pos) {
		return false
	}
	ok := h.cursor.DeleteSelection()
	h.cursor.Unselect()
	return ok
}

// deleteToLineEnd implements C-k, deleting the line ending itself when
// the caret already sits past the last cell.
func (h *helixHandlerImpl) deleteToLineEnd() bool {
	pos := h.cursor.CursorAtScroll()
	buf := h.less.Buffer()
	if pos.Y >= buf.Rows() {
		return false
	}
	end := term.Coordinates{Y: pos.Y, X: buf.Columns(pos.Y)}
	if pos.X >= end.X {
		// The caret already sits on the line separator, so kill_to_line_end
		// swallows it and pulls the next line up.
		return h.cursor.Conflate()
	}
	h.cursor.Unselect()
	h.explicitSel = false
	if !h.cursor.SelectRange(pos, end) {
		return false
	}
	ok := h.cursor.DeleteSelection()
	h.cursor.Unselect()
	h.cursor.MoveToScroll(pos)
	return ok
}

// backspace implements delete_char_backward. Helix tries a dedent
// first: when only indentation precedes the caret, backspace removes a
// whole indent level instead of a single space.
func (h *helixHandlerImpl) backspace() bool {
	if h.dedent() {
		return true
	}
	if h.config.autoPair {
		return h.cursor.BackspaceAutoPair()
	}
	return h.cursor.Backspace()
}

// dedent removes up to one indent level of spaces before the caret,
// mirroring helix-term's dedent helper. A tab takes the fast path of a
// plain backspace, and a line that is not purely indented so far is
// left to the caller.
func (h *helixHandlerImpl) dedent() bool {
	pos := h.cursor.CursorAtScroll()
	if pos.X == 0 || h.config.indentRune != text.IndentRuneSpace {
		return false
	}
	width := h.config.indentTabspaces
	if width <= 0 {
		width = h.config.tabspaces
	}
	if width <= 1 {
		return false
	}
	line := []rune(rowString(h, pos.Y))
	if pos.X > len(line) {
		return false
	}
	for _, ch := range line[:pos.X] {
		if ch != ' ' && ch != '\t' {
			return false
		}
	}
	drop := pos.X % width
	if drop == 0 {
		drop = width
	}
	start := pos.X
	for range drop {
		if start == 0 || line[start-1] != ' ' {
			break
		}
		start--
	}
	if start == pos.X {
		return false
	}
	h.cursor.Unselect()
	h.explicitSel = false
	if !h.cursor.SelectRange(term.Coordinates{X: start, Y: pos.Y}, pos) {
		return false
	}
	ok := h.cursor.DeleteSelection()
	h.cursor.Unselect()
	return ok
}

// deleteWordForward implements A-d in insert mode.
func (h *helixHandlerImpl) deleteWordForward() bool {
	origin := h.cursor.CursorAtScroll()
	h.cursor.Unselect()
	h.explicitSel = false
	if !h.cursor.MoveRightEndWord() {
		return false
	}
	end := h.cursor.CursorAtScroll()
	if next, ok := h.nextCoord(end); ok {
		end = next
	}
	h.cursor.MoveToScroll(origin)
	if !h.cursor.SelectRange(origin, end) {
		return false
	}
	ok := h.cursor.DeleteSelection()
	h.cursor.Unselect()
	return ok
}

func (h *helixHandlerImpl) firstNonBlank(row int) (term.Coordinates, bool) {
	buf := h.less.Buffer()
	if row < 0 || row >= buf.Rows() {
		return term.Coordinates{}, false
	}
	cells := buf.View().RawCells()
	if row >= len(cells) {
		return term.Coordinates{}, false
	}
	for x, c := range cells[row] {
		if c.Ch != ' ' && c.Ch != '\t' {
			return term.Coordinates{X: x, Y: row}, true
		}
	}
	return term.Coordinates{}, false
}

func (h *helixHandlerImpl) insertRegisterContents(name rune) bool {
	data, err := h.readRegister(name)
	if err != nil {
		h.logError(err)
		return false
	}
	if data.Text == "" {
		return false
	}
	h.cursor.InsertString(data.Text)
	h.insertRegister.WriteString(data.Text)
	return true
}

func (h *helixHandlerImpl) handleInsert(ev term.Event) (quit, handled bool) {
	if h.pendingInsertRegister {
		h.pendingInsertRegister = false
		if ev.Ch == 0 && ev.Key == 0 {
			return false, true
		}
		if ev.Mod == 0 && validRegisterName(ev.Ch) {
			return false, h.insertRegisterContents(normalizedRegisterName(ev.Ch))
		}
		return false, true
	}

	switch ev.Mod {
	case 0:
		switch ev.Key {
		case term.KeyEnter:
			if h.config.autoPair {
				h.cursor.InsertAutoPairNewline(h.config.indentRune, h.config.indentTabspaces)
			} else {
				h.cursor.InsertWithIndentRune('\n', h.config.indentRune, h.config.indentTabspaces)
			}
			h.insertRegister.WriteRune('\n')
			handled = true
		case term.KeySpace:
			h.cursor.InsertWithIndentRune(' ', h.config.indentRune, h.config.indentTabspaces)
			h.insertRegister.WriteRune(' ')
			handled = true
		case term.KeyTab:
			h.insertTabIndent()
			handled = true
		case term.KeyBackspace:
			h.backspace()
			handled = true
		case term.KeyDelete:
			h.cursor.Delete()
			handled = true
		case term.KeyEsc:
			h.exitInsert()
			handled = true
		case term.KeyArrowUp:
			h.cursor.MoveToScroll(h.anchor)
			handled = h.cursor.MoveUp()
		case term.KeyArrowRight:
			h.cursor.MoveToScroll(h.anchor)
			handled = h.cursor.MoveRight()
		case term.KeyArrowDown:
			h.cursor.MoveToScroll(h.anchor)
			handled = h.cursor.MoveDown()
		case term.KeyArrowLeft:
			h.cursor.MoveToScroll(h.anchor)
			handled = h.cursor.MoveLeft()
		case term.KeyHome:
			handled = h.cursor.MoveStartLine()
		case term.KeyEnd:
			handled = h.cursor.MoveEndLine()
		case term.KeyPgup:
			handled = h.cursor.MoveUpLines(max(1, h.less.Scroll().SizeHeight()-pagePadding))
		case term.KeyPgdn:
			handled = h.cursor.MoveDownLines(max(1, h.less.Scroll().SizeHeight()-pagePadding))
		default:
			if ev.Ch != 0 {
				if h.config.autoPair {
					h.cursor.InsertWithAutoPair(ev.Ch, h.config.indentRune, h.config.indentTabspaces)
				} else {
					h.cursor.InsertWithIndentRune(ev.Ch, h.config.indentRune, h.config.indentTabspaces)
				}
				h.insertRegister.WriteRune(ev.Ch)
				handled = true
			}
		}
	case term.ModShift:
		switch ev.Key {
		case term.KeyTab:
			h.insertIndentLiteral()
			handled = true
		case term.KeyBackspace:
			handled = h.backspace()
		}
	case term.ModAlt:
		switch ev.Key {
		case term.KeyBackspace:
			handled = h.cursor.BackspaceWord()
		case term.KeyDelete:
			handled = h.deleteWordForward()
		default:
			if ev.Ch == 'd' {
				handled = h.deleteWordForward()
			}
		}
	case term.ModCtrl:
		switch ev.Ch {
		case 'c':
			h.exitInsert()
			handled = true
		case 'h':
			handled = h.backspace()
		case 'w':
			handled = h.cursor.BackspaceWord()
		case 'u':
			handled = h.deleteToLineStart()
		case 'k':
			handled = h.deleteToLineEnd()
		case 'd':
			handled = h.cursor.Delete()
		case 'j':
			h.cursor.InsertWithIndentRune('\n', h.config.indentRune, h.config.indentTabspaces)
			h.insertRegister.WriteRune('\n')
			handled = true
		case 'r':
			h.pendingInsertRegister = true
			handled = true
		case 's':
			h.undoCheckpoint = true
			handled = true
		}
	}
	return quit, handled
}

// --- plumbing ---

// Selection satisfies tui.Handler.
func (h *helixHandlerImpl) Selection() (string, bool) {
	sel := h.cursor.Selection()
	return sel, sel != ""
}

func (h *helixHandlerImpl) cursorAtScroll() term.Coordinates {
	return h.cursor.CursorAtScroll()
}

func (h *helixHandlerImpl) setCursorAtScroll(pos term.Coordinates) bool {
	pos.Y = max(0, min(pos.Y, h.less.Buffer().Rows()-1))
	pos.X = max(0, min(pos.X, h.less.Buffer().Columns(pos.Y)))
	// Robust against resizes: only the first client interaction clears
	// this position.
	if h.less.Scroll().Width() == 0 || h.less.Scroll().SizeHeight() == 0 {
		h.pendingSetCursor = new(term.Coordinates)
		*h.pendingSetCursor = pos
		return false
	}
	_, ok := h.cursor.MoveToScroll(pos)
	h.anchor = h.cursorAtScroll()
	if h.mode() == normalMode && !h.extend {
		h.anchorHere()
	}
	h.markMatchingBrace()
	return ok
}

func (h *helixHandlerImpl) moveToNextLocation(ID string) bool {
	ok := h.cursor.MoveToNextLocation(ID)
	h.anchor = h.cursorAtScroll()
	h.markMatchingBrace()
	return ok
}

func (h *helixHandlerImpl) moveToPrevLocation(ID string) bool {
	ok := h.cursor.MoveToPrevLocation(ID)
	h.anchor = h.cursorAtScroll()
	h.markMatchingBrace()
	return ok
}

func (h *helixHandlerImpl) setLocationList(
	pri textapi.LocationPriority, ID string, l text.LocationList,
) {
	_ = h.cursor.SetLocationList(pri, ID, l)
}

func (h *helixHandlerImpl) OnWillSeek(from term.Coordinates) {}

func (h *helixHandlerImpl) OnDidSeek(from, to term.Coordinates) {}

func (h *helixHandlerImpl) OnWillHide(start, end int) {}

func (h *helixHandlerImpl) OnWillVisible(start int) {}

func (h *helixHandlerImpl) OnDidHide(start, end int) {
	h.config.scheduleNextTick(func() {
		h.anchor = h.cursorAtScroll()
	})
}

func (h *helixHandlerImpl) OnDidVisible(start int) {
	h.config.scheduleNextTick(func() {
		h.anchor = h.cursorAtScroll()
	})
}
