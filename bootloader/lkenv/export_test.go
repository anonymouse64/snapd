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

package lkenv

import "fmt"

var (
	CopyString  = copyString
	CToGoString = cToGoString
)

// GetBootPartition returns the first found boot partition that contains a
// reference to the given kernel revision. If the revision was not found, a
// non-nil error is returned.
func (l *Env) GetBootPartition(kernel string) (string, error) {
	var matr bootimgKernelMatrix
	switch l.version {
	case V1:
		matr = l.env_v1.Bootimg_matrix
	default:
		panic("test function unimplemented for non-v1")
	}
	for x := range matr {
		if kernel == cToGoString(matr[x][MATRIX_ROW_KERNEL][:]) {
			return cToGoString(matr[x][MATRIX_ROW_PARTITION][:]), nil
		}
	}
	return "", fmt.Errorf("cannot find kernel %q in boot image partitions", kernel)
}
