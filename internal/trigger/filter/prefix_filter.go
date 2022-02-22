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

package filter

import (
	"fmt"
	ce "github.com/cloudevents/sdk-go/v2"
	"github.com/linkall-labs/vanus/observability/log"
	"strings"
)

type prefixFilter struct {
	attribute, prefix string
}

func NewPrefixFilter(attribute, prefix string) Filter {
	if attribute == "" || prefix == "" {
		return nil
	}
	return &prefixFilter{attribute: attribute, prefix: prefix}
}

func (filter *prefixFilter) Filter(event ce.Event) FilterResult {
	if filter == nil {
		return FailFilter
	}
	log.Debug("prefix filter ", map[string]interface{}{"filter": filter, "event": event})
	value, ok := LookupAttribute(event, filter.attribute)
	if !ok {
		return FailFilter
	}

	if strings.HasPrefix(fmt.Sprintf("%v", value), filter.prefix) {
		return PassFilter
	}
	return FailFilter
}

func (filter *prefixFilter) String() string {
	return fmt.Sprintf("attribute:%s,prefix:%s", filter.attribute, filter.prefix)
}

var _ Filter = (*prefixFilter)(nil)
