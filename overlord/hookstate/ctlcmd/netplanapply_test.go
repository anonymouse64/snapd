// -*- Mode: Go; indent-tabs-mode: t -*-

/*
 * Copyright (C) 2019 Canonical Ltd
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

package ctlcmd_test

import (
	. "gopkg.in/check.v1"

	"github.com/snapcore/snapd/asserts"
	"github.com/snapcore/snapd/asserts/sysdb"
	"github.com/snapcore/snapd/dirs"
	"github.com/snapcore/snapd/interfaces"
	"github.com/snapcore/snapd/interfaces/builtin"
	"github.com/snapcore/snapd/overlord/hookstate"
	"github.com/snapcore/snapd/overlord/hookstate/ctlcmd"
	"github.com/snapcore/snapd/overlord/hookstate/hooktest"
	"github.com/snapcore/snapd/overlord/ifacestate/ifacerepo"
	"github.com/snapcore/snapd/overlord/snapstate"
	"github.com/snapcore/snapd/overlord/snapstate/snapstatetest"
	"github.com/snapcore/snapd/overlord/state"
	"github.com/snapcore/snapd/snap"
	"github.com/snapcore/snapd/testutil"
)

type netplanApplyCtlSuite struct {
	testutil.BaseTest
	st                 *state.State
	fakeStore          fakeStore
	yesMockContext     *hookstate.Context
	missingMockContext *hookstate.Context
	noMockContext      *hookstate.Context
	invalidMockContext *hookstate.Context
	mockHandler        *hooktest.MockHandler
}

var _ = Suite(&netplanApplyCtlSuite{})

const canUseSnapYaml = `name: test-snap-yes-true
version: 1.0
summary: test-snap
plugs:
 net-setup:
  interface: network-setup-control
  netplan-apply: "true"
apps:
 netplan-apply:
  command: bin/dummy
  plugs: [net-setup]
`

const missingCannotUseSnapYaml = `name: test-snap-no-missing
version: 1.0
summary: test-snap
plugs:
 net-setup:
  interface: network-setup-control
apps:
 netplan-apply:
  command: bin/dummy
  plugs: [net-setup]
`

const presentCannotUseSnapYaml = `name: test-snap-no-false
version: 1.0
summary: test-snap
plugs:
 net-setup:
  interface: network-setup-control
  netplan-apply: "false"
apps:
 netplan-apply:
  command: bin/dummy
  plugs: [net-setup]
`

const invalidCannotUseSnapYaml = `name: test-snap-no-invalid
version: 1.0
summary: test-snap
plugs:
 net-setup:
  interface: network-setup-control
  netplan-apply: invalid
apps:
 netplan-apply:
  command: bin/dummy
  plugs: [net-setup]
`

const coreYaml = `name: core
version: 1.0
type: os
slots:
 network-setup-control:
`

func (s *netplanApplyCtlSuite) SetUpTest(c *C) {
	s.BaseTest.SetUpTest(c)
	oldRoot := dirs.GlobalRootDir
	dirs.SetRootDir(c.MkDir())

	testutil.MockCommand(c, "netplan", "")

	s.BaseTest.AddCleanup(func() {
		dirs.SetRootDir(oldRoot)
	})
	s.BaseTest.AddCleanup(snap.MockSanitizePlugsSlots(func(snapInfo *snap.Info) {}))

	s.mockHandler = hooktest.NewMockHandler()

	s.st = state.New(nil)
	s.st.Lock()
	defer s.st.Unlock()

	snapstate.ReplaceStore(s.st, &s.fakeStore)

	// mock installed snaps
	info1 := snapstatetest.MockSnapCurrent(c, s.st, canUseSnapYaml)
	snapstatetest.MockSnapCurrent(c, s.st, missingCannotUseSnapYaml)
	snapstatetest.MockSnapCurrent(c, s.st, presentCannotUseSnapYaml)
	snapstatetest.MockSnapCurrent(c, s.st, invalidCannotUseSnapYaml)

	yesTask := s.st.NewTask("test-snap-yes-true-task", "my test task")
	yesSetup := &hookstate.HookSetup{Snap: "test-snap-yes-true", Revision: snap.R(1), Hook: "test-snap-yes-true-hook"}

	var err error
	s.yesMockContext, err = hookstate.NewContext(yesTask, yesTask.State(), yesSetup, s.mockHandler, "")
	c.Assert(err, IsNil)

	missingTask := s.st.NewTask("test-snap-no-missing-task", "my test task")
	missingSetup := &hookstate.HookSetup{Snap: "test-snap-no-missing", Revision: snap.R(1), Hook: "test-snap-no-missing-hook"}

	s.missingMockContext, err = hookstate.NewContext(missingTask, missingTask.State(), missingSetup, s.mockHandler, "")
	c.Assert(err, IsNil)

	noTask := s.st.NewTask("test-snap-no-false-task", "my test task")
	noSetup := &hookstate.HookSetup{Snap: "test-snap-no-false", Revision: snap.R(1), Hook: "test-snap-no-false-hook"}

	s.noMockContext, err = hookstate.NewContext(noTask, noTask.State(), noSetup, s.mockHandler, "")
	c.Assert(err, IsNil)

	invalidTask := s.st.NewTask("test-snap-no-invalid-task", "my test task")
	invalidSetup := &hookstate.HookSetup{Snap: "test-snap-no-invalid", Revision: snap.R(1), Hook: "test-snap-no-invalid-hook"}

	s.invalidMockContext, err = hookstate.NewContext(invalidTask, invalidTask.State(), invalidSetup, s.mockHandler, "")
	c.Assert(err, IsNil)

	s.st.Set("seeded", true)
	s.st.Set("refresh-privacy-key", "privacy-key")
	snapstate.Model = func(*state.State) (*asserts.Model, error) {
		return sysdb.GenericClassicModel(), nil
	}

	core1 := snapstatetest.MockSnapCurrent(c, s.st, coreYaml)
	c.Assert(core1.Slots, HasLen, 1)

	repo := interfaces.NewRepository()
	for _, iface := range builtin.Interfaces() {
		err := repo.AddInterface(iface)
		c.Assert(err, IsNil)
	}
	err = repo.AddSnap(info1)
	c.Assert(err, IsNil)
	err = repo.AddSnap(core1)
	c.Assert(err, IsNil)
	_, err = repo.Connect(&interfaces.ConnRef{
		PlugRef: interfaces.PlugRef{Snap: info1.InstanceName(), Name: "net-setup"},
		SlotRef: interfaces.SlotRef{Snap: core1.InstanceName(), Name: "network-setup-control"},
	}, nil, nil, nil, nil, nil)
	c.Assert(err, IsNil)
	conns, err := repo.Connected("test-snap-yes-true", "net-setup")
	c.Assert(err, IsNil)
	c.Assert(conns, HasLen, 1)
	ifacerepo.Replace(s.st, repo)

}

func (s *netplanApplyCtlSuite) TestYesNetplanApply(c *C) {
	_, _, err := ctlcmd.Run(s.yesMockContext, []string{"netplan-apply"}, 0)
	c.Assert(err, IsNil)
}

func (s *netplanApplyCtlSuite) TestMissingNetplanApply(c *C) {
	_, _, err := ctlcmd.Run(s.missingMockContext, []string{"netplan-apply"}, 0)
	c.Assert(err, NotNil)
	c.Assert(err, ErrorMatches, `cannot use netplan apply - must have network-setup-control interface connected with netplan-apply attribute specified as true`)
}

func (s *netplanApplyCtlSuite) TestNoNetplanApply(c *C) {
	_, _, err := ctlcmd.Run(s.noMockContext, []string{"netplan-apply"}, 0)
	c.Assert(err, NotNil)
	c.Assert(err, ErrorMatches, `cannot use netplan apply - must have network-setup-control interface connected with netplan-apply attribute specified as true`)
}

func (s *netplanApplyCtlSuite) TestInvalidNetplanApply(c *C) {
	_, _, err := ctlcmd.Run(s.invalidMockContext, []string{"netplan-apply"}, 0)
	c.Assert(err, NotNil)
	c.Assert(err, ErrorMatches, `invalid setting for netplan-apply, must be true/false`)
}
