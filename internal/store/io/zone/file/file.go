// Copyright 2022 Linkall Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package file

import (
	// standard libraries.
	"os"

	// this project.
	"github.com/linkall-labs/vanus/internal/store/io/zone"
)

type file struct {
	f *os.File
}

// Make sure file implements zone.Interface.
var _ zone.Interface = (*file)(nil)

func New(f *os.File) (zone.Interface, error) {
	return &file{
		f: f,
	}, nil
}

func (f *file) Raw(off int64) (*os.File, int64) {
	return f.f, off
}
