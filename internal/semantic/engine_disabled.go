// Copyright (C) 2026 Massimo Cavalleri <massimo.cavalleri@gmail.com>
//
// This file is part of shfm.
//
// shfm is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// shfm is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU General Public License for more details.
//
// You should have received a copy of the GNU General Public License
// along with shfm.  If not, see <https://www.gnu.org/licenses/>.

//go:build !semantic

package semantic

import "context"

func init() { Available = false }

type disabledEngine struct{}

func newEngine() Engine { return disabledEngine{} }

func (disabledEngine) EnsureIndex(_ context.Context, _ string, statusCh chan<- Status) {
	defer close(statusCh)
	statusCh <- Status{Err: ErrUnavailable, Finished: true}
}

func (disabledEngine) HasIndex(_ string) bool { return false }

func (disabledEngine) Search(_ context.Context, _, _ string, _ int) ([]Result, error) {
	return nil, ErrUnavailable
}
