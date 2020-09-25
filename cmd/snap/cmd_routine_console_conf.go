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

package main

import (
	"time"

	"github.com/jessevdk/go-flags"
	"github.com/snapcore/snapd/i18n"
)

type cmdRoutineConsoleConfStart struct {
	clientMixin
}

var shortRoutineConsoleConfStartHelp = i18n.G("Start console-conf snapd routine")
var longRoutineConsoleConfStartHelp = i18n.G(`
The console-conf-start command starts synchronization with console-conf

This command is used by console-conf when it starts up. It delays refreshes if
there are none currently ongoing, and exits with a specific error code if there
are ongoing refreshes which console-conf should wait for before prompting the 
user to begin configuring the device.
`)

func init() {
	addRoutineCommand("console-conf-start", shortRoutineConsoleConfStartHelp, longRoutineConsoleConfStartHelp, func() flags.Commander {
		return &cmdRoutineConsoleConfStart{}
	}, nil, nil)
}

func (x *cmdRoutineConsoleConfStart) Execute(args []string) error {

	chgs, err := x.client.ConsoleConfStart()
	if err != nil {
		return err
	}

	// wait for all the changes that were returned
	for _, chgID := range chgs {
		// loop infinitely until the change is done
		for {
			chgDone := false
			chg, err := queryChange(x.client, chgID)
			if err != nil {
				return err
			}

			switch chg.Status {
			case "Done", "Undone", "Hold", "Error":
				chgDone = true
			}
			if chgDone {
				break
			}

			// let's not DDOS snapd, 0.5 Hz should be fast enough
			time.Sleep(2 * time.Second)
		}
	}

	return nil
}
