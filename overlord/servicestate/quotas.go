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

package servicestate

import (
	"github.com/snapcore/snapd/overlord/state"
	"github.com/snapcore/snapd/quota"
)

func AllQuotas(st *state.State) (map[string]*quota.Group, error) {
	var quotas map[string]*quota.Group
	if err := st.Get("quotas", &quotas); err != nil {
		if err != state.ErrNoState {
			return nil, err
		}
		// otherwise there are no quotas so just return nil
		return nil, nil
	}

	// quota groups are not serialized with all the necessary tracking
	// information in the objects, so we need to thread some things around
	if err := quota.CompleteCrossReferences(quotas); err != nil {
		return nil, err
	}

	// quotas has now been properly initialized with unexported cross-references
	return quotas, nil
}

func GetQuota(st *state.State, name string) (*quota.Group, error) {
	allGrps, err := AllQuotas(st)
	if err != nil {
		return nil, err
	}

	// if the referenced group does not exist we return a nil group
	return allGrps[name], nil
}

func SetQuota(st *state.State, grp *quota.Group) error {
	// get all the quotas
	allGrps, err := AllQuotas(st)
	if err != nil {
		// All() can't return ErrNoState, in that case it just returns a nil
		// map, which we handle below
		return err
	}
	if allGrps == nil {
		allGrps = make(map[string]*quota.Group)
	}

	allGrps[grp.Name] = grp

	st.Set("quotas", allGrps)
	return nil
}
