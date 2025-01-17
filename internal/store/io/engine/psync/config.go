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

package psync

const (
	defaultWriteTaskBufferSize = 64
	defaultParallel            = 4
)

type config struct {
	writeTaskBufferSize int
	parallel            int
}

func defaultConfig() config {
	cfg := config{
		writeTaskBufferSize: defaultWriteTaskBufferSize,
		parallel:            defaultParallel,
	}
	return cfg
}

type Option func(*config)

func makeConfig(opts ...Option) config {
	cfg := defaultConfig()
	for _, opt := range opts {
		opt(&cfg)
	}
	return cfg
}

func WithWriteTaskBufferSize(size int) Option {
	return func(cfg *config) {
		cfg.writeTaskBufferSize = size
	}
}

func WithParallel(parallel int) Option {
	return func(cfg *config) {
		cfg.parallel = parallel
	}
}
