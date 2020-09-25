// -*- Mode: Go; indent-tabs-mode: t -*-

/*
 * Copyright (C) 2020 Canonical Ltd
 *
 * This program is free software: you can redistribute it and/or modify
 * it under the terms of the GNU General Public License version 3 as
 * published by the Free Software Foundation.
 *
 * This program is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 * GNU General Public License for more details.
 *
 * You should have received a copy of the GNU General Public License
 * along with this program.  If not, see <http://www.gnu.org/licenses/>.
 *
 */

package daemon

import (
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/snapcore/snapd/overlord/auth"
)

var (
	routineConsoleConfStartCmd = &Command{
		Path: "/v2/internal/console-conf-start",
		POST: consoleConfStartRoutine,
	}
)

type consoleConfRoutine struct{}

// ConsoleConfStartRoutineResult is the result of running the console-conf start
// routine..
type ConsoleConfStartRoutineResult struct {
	ActiveSnapAutoRefreshChanges []string `json:"active-snap-refreshes,omitempty"`
}

func consoleConfStartRoutine(c *Command, r *http.Request, _ *auth.UserState) Response {
	// no body expected, error if we were provided anything
	defer r.Body.Close()
	var routineBody interface{}
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(&routineBody); err != nil && err != io.EOF {
		return BadRequest("cannot decode request body into console-conf operation: %v", err)
	}

	if routineBody != nil {
		return BadRequest("unsupported console-conf routine POST request body")
	}

	// now run the start routine first by trying to grab a lock on the refreshes
	// for all snaps, which fails if there are any active changes refreshing
	// snaps
	st := c.d.overlord.State()
	st.Lock()
	defer st.Unlock()

	// TODO: would be nice to be able to display what snaps are involved in the
	// specified changes
	snapAutoRefreshChanges, err := c.d.overlord.SnapManager().EnsureAutoRefreshesAreDelayed(20 * time.Minute)
	if err != nil {
		return InternalError(err.Error())
	}

	if len(snapAutoRefreshChanges) == 0 {
		// no changes yet, and we delayed the refresh successfully so
		// console-conf is okay to run normally
		return SyncResponse(&ConsoleConfStartRoutineResult{}, nil)
	}

	chgIds := make([]string, 0, len(snapAutoRefreshChanges))
	for _, chg := range snapAutoRefreshChanges {
		chgIds = append(chgIds, chg.ID())
	}

	// we have changes that the client should wait for before being ready
	return SyncResponse(&ConsoleConfStartRoutineResult{
		ActiveSnapAutoRefreshChanges: chgIds,
	}, nil)
}
