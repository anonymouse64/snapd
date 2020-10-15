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

package bootloader

import (
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"

	"github.com/snapcore/snapd/bootloader/lkenv"
	"github.com/snapcore/snapd/dirs"
	"github.com/snapcore/snapd/logger"
	"github.com/snapcore/snapd/snap"
)

type lk struct {
	rootdir       string
	inRuntimeMode bool

	// role is what bootloader role we are, which also maps to which version of
	// the underlying lkenv struct we use for bootenv
	// * RoleSole == uc16 -> v1
	// * RoleRecovery == uc20 + recovery -> v2 recovery
	// * RoleRunMode == uc20 + run -> v2 run
	role Role
}

func (l *lk) processOpts(opts *Options) {
	if opts != nil {
		// XXX: in the long run we want this to go away, we probably add
		//      something like "boot.PrepareImage()" and add an (optional)
		//      method "PrepareImage" to the bootloader interface that is
		//      used to setup a bootloader from prepare-image if things
		//      are very different from runtime vs image-building mode.
		//
		// determine mode we are in, runtime or image build

		// TODO: can we get rid of this now that we have roles? seems like no :-/
		l.inRuntimeMode = !opts.PrepareImageTime

		l.role = opts.Role
	}
}

// newLk create a new lk bootloader object
func newLk(rootdir string, opts *Options) Bootloader {
	l := &lk{rootdir: rootdir}

	l.processOpts(opts)

	return l
}

func (l *lk) setRootDir(rootdir string) {
	l.rootdir = rootdir
}

func (l *lk) Name() string {
	return "lk"
}

func (l *lk) dir() string {
	// we have two scenarios, image building and runtime
	// during image building we store environment into file
	// at runtime environment is written directly into dedicated partition
	if l.inRuntimeMode {
		switch l.role {
		case RoleSole:
			return filepath.Join(l.rootdir, "/dev/disk/by-partlabel/")
		case RoleRecovery, RoleRunMode:
			// for uc20 roles, RunMode and Recovery, we ignore the root dir
			// provided and instead use dirs.GlobalRootDir, because for example
			// in install mode the provided dir will be "/run/mnt/ubuntu-seed",
			// but our dir we care about is "/dev/disk/by-partlabel" which even
			// for the recovery case will always live underneath "/" as /dev is
			// not also mounted on /run/mnt/ubuntu-seed
			return filepath.Join(dirs.GlobalRootDir, "/dev/disk/by-partlabel/")
		default:
			panic("unexpected bootloader role for lk dir")
		}
	}
	return filepath.Join(l.rootdir, "/boot/lk/")
}

func (l *lk) InstallBootConfig(gadgetDir string, opts *Options) (bool, error) {
	// make sure that the opts are put into the object
	l.processOpts(opts)
	gadgetFile := filepath.Join(gadgetDir, l.Name()+".conf")
	systemFile := l.ConfigFile()
	return genericInstallBootConfig(gadgetFile, systemFile)
}

func (l *lk) ConfigFile() string {
	return l.envFile()
}

func (l *lk) envFile() string {
	// as for dir, we have two scenarios, image building and runtime
	if l.inRuntimeMode {
		// TO-DO: this should be eventually fetched from gadget.yaml
		switch l.role {
		case RoleSole, RoleRunMode:
			// for run mode, see the comment in dir(), we actually use different
			// dirs for RoleSole and RoleRunMode
			return filepath.Join(l.dir(), "snapbootsel")
		case RoleRecovery:
			// recovery bl env file is different
			return filepath.Join(l.dir(), "snaprecoverysel")
		}
	}

	switch l.role {
	case RoleSole, RoleRunMode:
		return filepath.Join(l.dir(), "snapbootsel.bin")
	case RoleRecovery:
		return filepath.Join(l.dir(), "snaprecoverysel.bin")
	}
	panic("unknown bootloader role in lk envFile()")
}

func (l *lk) GetBootVars(names ...string) (map[string]string, error) {
	out := make(map[string]string)

	env := l.newenv()
	if err := env.Load(); err != nil {
		return nil, err
	}

	for _, name := range names {
		out[name] = env.Get(name)
	}

	return out, nil
}

func (l *lk) newenv() *lkenv.Env {
	// check which role we are, it affects which struct is used for the env
	var version lkenv.Version
	switch l.role {
	case RoleSole:
		version = lkenv.V1
	case RoleRecovery:
		version = lkenv.V2Recovery
	case RoleRunMode:
		version = lkenv.V2Run
	}
	return lkenv.NewEnv(l.envFile(), version)
}

func (l *lk) SetBootVars(values map[string]string) error {
	env := l.newenv()
	if err := env.Load(); err != nil && !os.IsNotExist(err) {
		return err
	}

	// update environment only if something change
	dirty := false
	for k, v := range values {
		// already set to the right value, nothing to do
		if env.Get(k) == v {
			continue
		}
		env.Set(k, v)
		dirty = true
	}

	if dirty {
		return env.Save()
	}

	return nil
}

func (l *lk) ExtractRecoveryKernelAssets(recoverySystemDir string, sn snap.PlaceInfo, snapf snap.Container) error {
	logger.Debugf("ExtractRecoveryKernelAssets (%s)", recoverySystemDir)
	env := l.newenv()
	if err := env.Load(); err != nil && !os.IsNotExist(err) {
		return err
	}

	// recoverySystemDir includes leading dir where recovery systems are stored
	// this information is not relevant to lk, and it breaks mapping of
	// Snapd_recovery_system value to correct recovery partition
	recoverySystem := filepath.Base(recoverySystemDir)

	bootPartition, err := env.FindFreeRecoverySystemPartition(recoverySystem)
	if err != nil {
		return err
	}

	if l.inRuntimeMode {
		// error case, we cannot be extracting a recovery kernel and also be
		// called with !opts.PrepareImageTime

		// TODO:UC20: however this codepath will likely be exercised when we
		//            support creating new recovery systems
		return fmt.Errorf("internal error: ExtractRecoveryKernelAssets does not make sense with a runtime lk bootloader")
	}

	// we are preparing a recovery system, just extract boot image to bootloader
	// directory
	logger.Debugf("ExtractRecoveryKernelAssets handling image prepare")
	if err := snapf.Unpack(env.GetBootImageName(), l.dir()); err != nil {
		return fmt.Errorf("cannot open unpacked %s: %v", env.GetBootImageName(), err)
	}

	if err := env.SetRecoverySystemBootPartition(bootPartition, recoverySystem); err != nil {
		return err
	}

	return env.Save()
}

// ExtractKernelAssets extract kernel assets per bootloader specifics
// lk bootloader requires boot partition to hold valid boot image
// there are two boot partition available, one holding current bootimage
// kernel assets are extracted to other (free) boot partition
// in case this function is called as part of image creation,
// boot image is extracted to the file
func (l *lk) ExtractKernelAssets(s snap.PlaceInfo, snapf snap.Container) error {
	blobName := s.Filename()

	logger.Debugf("ExtractKernelAssets (%s)", blobName)

	env := l.newenv()
	if err := env.Load(); err != nil && !os.IsNotExist(err) {
		return err
	}

	bootPartition, err := env.FindFreeBootPartition(blobName)
	if err != nil {
		return err
	}

	if l.inRuntimeMode {
		// this is live system, extracted bootimg needs to be flashed to
		// free bootimg partition and env has to be updated with
		// new kernel snap to bootimg partition mapping
		if err := l.extractBootImageToPartition(bootPartition, env, snapf); err != nil {
			return err
		}
	} else {
		// we are preparing image, just extract boot image to bootloader directory
		logger.Debugf("ExtractKernelAssets handling image prepare")
		if err := snapf.Unpack(env.GetBootImageName(), l.dir()); err != nil {
			return fmt.Errorf("cannot open unpacked %s: %v", env.GetBootImageName(), err)
		}
	}
	if err := env.SetBootPartition(bootPartition, blobName); err != nil {
		return err
	}

	return env.Save()
}

func (l *lk) RemoveKernelAssets(s snap.PlaceInfo) error {
	blobName := s.Filename()
	logger.Debugf("RemoveKernelAssets (%s)", blobName)
	env := l.newenv()
	if err := env.Load(); err != nil && !os.IsNotExist(err) {
		return err
	}
	err := env.RemoveKernelRevisionFromBootPartition(blobName)
	if err == nil {
		// found and removed the revision from the bootimg matrix, need to
		// update the env to persist the change
		return env.Save()
	}
	return nil
}

// extractBootImageToPartition helper function to extract kernel bootimage
// to passed boot partition
func (l *lk) extractBootImageToPartition(bootPartition string, env *lkenv.Env, snapf snap.Container) error {
	logger.Debugf("extractBootImageToPartition (%s)", bootPartition)
	tmpdir, err := ioutil.TempDir("", "bootimg")
	if err != nil {
		return fmt.Errorf("cannot create temp directory: %v", err)
	}
	defer os.RemoveAll(tmpdir)

	bootImg := env.GetBootImageName()
	if err := snapf.Unpack(bootImg, tmpdir); err != nil {
		return fmt.Errorf("cannot unpack %s: %v", bootImg, err)
	}
	// write boot.img to free boot partition
	bootimgName := filepath.Join(tmpdir, bootImg)
	bif, err := os.Open(bootimgName)
	if err != nil {
		return fmt.Errorf("cannot open unpacked %s: %v", bootImg, err)
	}
	defer bif.Close()
	bpart := filepath.Join(l.dir(), bootPartition)

	bpf, err := os.OpenFile(bpart, os.O_WRONLY, 0660)
	if err != nil {
		return fmt.Errorf("cannot open boot partition [%s]: %v", bpart, err)
	}
	defer bpf.Close()

	if _, err := io.Copy(bpf, bif); err != nil {
		return err
	}
	return nil
}
