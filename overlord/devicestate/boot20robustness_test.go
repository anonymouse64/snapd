// -*- Mode: Go; indent-tabs-mode: t -*-

/*
 * Copyright (C) 2016-2020 Canonical Ltd
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

package devicestate_test

import (
	"fmt"

	"github.com/snapcore/snapd/boot"
	"github.com/snapcore/snapd/dirs"
	"github.com/snapcore/snapd/overlord/auth"
	"github.com/snapcore/snapd/overlord/devicestate"
	"github.com/snapcore/snapd/overlord/devicestate/devicestatetest"
	"github.com/snapcore/snapd/overlord/ifacestate"
	"github.com/snapcore/snapd/overlord/state"
	"github.com/snapcore/snapd/timings"
	. "gopkg.in/check.v1"
)

var (
	_ = Suite(&bootRobustnessSuite{})
)

type bootRobustnessSuite struct {
	d *deviceMgrBaseSuite

	firstBootBaseTest

	b *boot20BaseTest
}

func (s *bootRobustnessSuite) SetUpTest(c *C) {
	// setup device manager
	s.d = &deviceMgrBaseSuite{}
	s.d.SetUpTest(c)

	// setup boot 20 seed snaps and such
	s.b = &boot20BaseTest{}
	s.b.setupBoot20Base()

	s.setupBaseTest(c, &s.b.TestingSeed20.SeedSnaps)

	// don't start the overlord here so that we can mock different modeenvs
	// later, which is needed by devicestart manager startup with uc20 booting

	s.b.SeedDir = dirs.SnapSeedDir

	// mock the snap mapper as snapd here
	s.d.AddCleanup(ifacestate.MockSnapMapper(&ifacestate.CoreSnapdSystemMapper{}))
}

// 	s.setupBoot20Base()

// 	s.TestingSeed20.SeedSnaps.SetupAssertSigning("canonical")
// 	s.TestingSeed20.SeedSnaps.Brands.Register("my-brand", brandPrivKey, map[string]interface{}{
// 		"verification": "verified",
// 	})

// 	s.devAcct = assertstest.NewAccount(s.StoreSigning, "developer", map[string]interface{}{
// 		"account-id": "developerid",
// 	}, "")

// 	s.AddCleanup(sysdb.InjectTrusted([]asserts.Assertion{s.StoreSigning.TrustedKey}))
// 	s.AddCleanup(ifacestate.MockSecurityBackends(nil))

// 	s.perfTimings = timings.New(nil)

// 	// mock the snap mapper as snapd here
// 	s.AddCleanup(ifacestate.MockSnapMapper(&ifacestate.CoreSnapdSystemMapper{}))
// }

func (s *bootRobustnessSuite) setPC20ModelInState(c *C) {
	s.b.setup20SeedSnaps(c)

	s.d.state.Lock()
	defer s.d.state.Unlock()

	s.d.makeModelAssertionInState(c, "canonical", "pc", map[string]interface{}{
		"display-name": "my model",
		"architecture": "amd64",
		"base":         "core20",
		"snaps": []interface{}{
			map[string]interface{}{
				"name":            "pc-kernel",
				"id":              s.b.AssertedSnapID("pc-kernel"),
				"type":            "kernel",
				"default-channel": "20",
			},
			map[string]interface{}{
				"name":            "pc",
				"id":              s.b.AssertedSnapID("pc"),
				"type":            "gadget",
				"default-channel": "20",
			}},
	})

	devicestatetest.SetDevice(s.d.state, &auth.DeviceState{
		Brand:  "canonical",
		Model:  "pc",
		Serial: "serialserialserial",
	})
}

func (s *bootRobustnessSuite) TestHappyMarkBootSuccessfulKernelUpgradeSetBootVarsPanics(c *C) {
	s.setPC20ModelInState(c)

	// s.bootloader.SetBootVars(map[string]string{
	// 	"snap_mode":     "trying",
	// 	"snap_try_core": "core_1.snap",
	// })

	// s.state.Lock()
	// defer s.state.Unlock()

	// dev, err := devicestate.DeviceCtx(s.state, nil, nil)
	// c.Assert(err, IsNil)

	// fmt.Println(dev.HasModeenv())

	// siCore1 := &snap.SideInfo{RealName: "core", Revision: snap.R(1)}
	// snapstate.Set(s.state, "core", &snapstate.SnapState{
	// 	SnapType: "os",
	// 	Active:   true,
	// 	Sequence: []*snap.SideInfo{siCore1},
	// 	Current:  siCore1.Revision,
	// })

	// s.state.Unlock()
	// err = devicestate.EnsureBootOk(s.mgr)
	// s.state.Lock()
	// c.Assert(err, IsNil)

	// m, err := s.bootloader.GetBootVars("snap_mode")
	// c.Assert(err, IsNil)
	// c.Assert(m, DeepEquals, map[string]string{"snap_mode": ""})

	n := 0
	restore := devicestate.MockPopulateStateFromSeed(func(st *state.State, opts *devicestate.PopulateStateFromSeedOptions, tm timings.Measurer) (ts []*state.TaskSet, err error) {
		c.Assert(opts, NotNil)
		c.Check(opts.Label, Equals, "20191127")
		c.Check(opts.Mode, Equals, "run")

		t := s.d.state.NewTask("test-task", "a random task")
		ts = append(ts, state.NewTaskSet(t))

		n++
		return ts, nil
	})
	defer restore()

	// mock the modeenv file
	m := boot.Modeenv{
		Mode:           "run",
		RecoverySystem: "20191127",
		Base:           "core20_1.snap",
	}
	err := m.Write("")
	c.Assert(err, IsNil)

	restoreBootloaderPanic := s.d.bootloader.SetRunKernelImagePanic("GetBootVars")
	defer restoreBootloaderPanic()

	// re-create manager so that modeenv file is-read
	s.d.mgr, err = devicestate.Manager(s.d.state, s.d.hookMgr, s.d.o.TaskRunner(), s.d.newStore)
	c.Assert(err, IsNil)

	err = devicestate.EnsureSeeded(s.d.mgr)
	c.Assert(err, IsNil)

	s.d.state.Lock()
	// don't defer the unlock here because it will get run when we panic below

	c.Check(s.d.state.Changes(), HasLen, 1)
	c.Check(n, Equals, 1)

	s.d.state.Unlock()
	fmt.Println("about to panic")
	c.Assert(func() { devicestate.EnsureBootOk(s.d.mgr) }, PanicMatches, "foobar")
	fmt.Println("panic'd")
	s.d.state.Lock()
	c.Assert(err, IsNil)
}
