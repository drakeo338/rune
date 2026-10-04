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

package remotescheme

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// TestKeepaliveContract pins the invariant that ties a tunneled gRPC
// client's ping cadence to the server's enforcement policy. If a future
// edit lowers ClientKeepalive.Time below ServerEnforcement.MinTime, or
// flips PermitWithoutStream off, the server would answer the client's
// keepalive pings with GOAWAY too_many_pings and tear down the single
// HTTP/2 connection carried over the tunnel — dropping terminals,
// invalidating cached pty fds, and forcing a reconnect.
func TestKeepaliveContract(t *testing.T) {
	assert.LessOrEqual(t, ServerEnforcement.MinTime, ClientKeepalive.Time,
		"server MinTime must permit the client ping interval, else "+
			"the server sends GOAWAY too_many_pings")
	assert.True(t, ServerEnforcement.PermitWithoutStream,
		"client pings with PermitWithoutStream, so the server must "+
			"permit pings without an active stream")
	assert.Greater(t, ServerEnforcement.MinTime, time.Duration(0),
		"MinTime must be a positive floor against a genuinely abusive peer")
}
