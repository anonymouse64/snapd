// -*- Mode: Go; indent-tabs-mode: t -*-

/*
 * Copyright (C) 2021 Canonical Ltd
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

package ctlcmd

import (
	"fmt"

	"github.com/snapcore/snapd/boot"
	"github.com/snapcore/snapd/i18n"
	"github.com/snapcore/snapd/overlord/devicestate"
	"github.com/snapcore/snapd/strutil"
	"gopkg.in/yaml.v2"
)

type systemModeCommand struct {
	baseCommand
}

var shortSystemModeHelp = i18n.G("Get the system mode")

var longSystemModeHelp = i18n.G(`
 The system-mode command returns information about the current system, including
 the system mode, whether the device is in factory mode, and where the run 
 system root filesystem is mounted in YAML format.
 The system mode will be one of run, recover, or install.
 Example output:

 $ snapctl system-mode
 system-mode: install
 factory: true
 host-ubuntu-data:
   - /run/mnt/ubuntu-data
`)

func init() {
	addCommand("system-mode", shortSystemModeHelp, longSystemModeHelp, func() command { return &systemModeCommand{} })
}

type systemModeResult struct {
	SystemMode    string   `yaml:"system-mode,omitempty"`
	Factory       bool     `yaml:"factory,omitempty"`
	RunDataRootfs []string `yaml:"host-ubuntu-data,omitempty"`
}

func (c *systemModeCommand) Execute(args []string) error {
	context := c.context()
	if context == nil {
		return fmt.Errorf("cannot run system-mode without a context")
	}

	// TODO: do we really need to lock the hook context here?
	// context.Lock()
	// defer context.Unlock()

	st := context.State()
	st.Lock()
	defer st.Unlock()
	t, _ := context.Task()
	dev, err := devicestate.DeviceCtx(st, t, nil)
	if err != nil {
		return err
	}

	res := systemModeResult{
		SystemMode: dev.SystemMode(),
	}

	// only get the other fields if the device has a modeenv
	if dev.HasModeenv() {
		// get the factory mode using the mode
		nextbootFlags, err := boot.BootFlags(dev)
		if err != nil {
			return err
		}
		if strutil.ListContains(nextbootFlags, "factory") {
			res.Factory = true
		}

		rootfsDirs, err := boot.HostUbuntuDataForMode(res.SystemMode)
		if err != nil {
			return err
		}
		res.RunDataRootfs = rootfsDirs
	}

	b, err := yaml.Marshal(res)
	if err != nil {
		return err
	}
	c.printf("%s\n", string(b))

	return nil
}
