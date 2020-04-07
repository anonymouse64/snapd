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

package partition_test

import (
	"fmt"

	. "gopkg.in/check.v1"

	"github.com/snapcore/snapd/boot"
	"github.com/snapcore/snapd/osutil"
	"github.com/snapcore/snapd/testutil"
)

type lsblkTestSuite struct {
	testutil.BaseTest
}

var _ = Suite(&lsblkTestSuite{})

func (s *lsblkTestSuite) SetUpTest(c *C) {
	s.BaseTest.SetUpTest(c)

	restore := osutil.MockMountInfo("")
	s.AddCleanup(restore)
}

func (s *lsblkTestSuite) TestDiskFromMountPointHappy(c *C) {
	// mock mountinfo for ubuntu-boot
	mountInfo := `
	892 29 252:3 / %s rw,relatime shared:343 - ext4 /dev/vda3 rw
	`
	restore := osutil.MockMountInfo(
		fmt.Sprintf(
			mountInfo[1:],
			boot.InitramfsUbuntuBootDir,
		),
	)
	defer restore()

	// mock lsblk when called on
	cmd := testutil.MockCommand(c, "lsblk", fmt.Sprintf(`echo '
		{
			"blockdevices": [
			  {
				"name": "vda",
				"children": [
				  {
					"name": "vda2",
					"fstype": "ext4",
					"label": "ubuntu-seed",
					"uuid": "a65a1bbb-4977-4945-b5a1-686b6bfe9a20",
					"fsavail": "581.9M",
					"fsuse%%": "12%%",
					"mountpoint": "%s",
					"partuuid": "some-uuid-thing"
				  }
				]
			  }
			]
		  }
	'`, boot.InitramfsUbuntuBootDir))
}
