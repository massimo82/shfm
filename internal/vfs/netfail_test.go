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

package vfs

import (
	"fmt"
	"io/fs"
	"testing"
)

type classifiedErr struct{ lost bool }

func (e classifiedErr) Error() string           { return "classified" }
func (e classifiedErr) ConnectionFailure() bool { return e.lost }

// TestIsConnectionFailureClassified: an error that says whether it's a
// lost connection is taken at its word, however wrapped.
func TestIsConnectionFailureClassified(t *testing.T) {
	for _, lost := range []bool{true, false} {
		err := &fs.PathError{Op: "stat", Path: "/x", Err: fmt.Errorf("wrapped: %w", classifiedErr{lost})}
		if got := IsConnectionFailure(err); got != lost {
			t.Errorf("IsConnectionFailure(%v) = %v, want %v", lost, got, lost)
		}
	}
}
