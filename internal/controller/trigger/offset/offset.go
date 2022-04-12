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

package offset

import (
	"context"
	"github.com/linkall-labs/vanus/internal/controller/trigger/storage"
	"github.com/linkall-labs/vanus/internal/primitive/info"
	"github.com/linkall-labs/vanus/observability/log"
	"sync"
	"time"
)

type Manager struct {
	subOffset sync.Map
	storage   storage.OffsetStorage
	ctx       context.Context
	stop      context.CancelFunc
}

func NewOffsetManager(storage storage.OffsetStorage) *Manager {
	m := &Manager{
		storage: storage,
	}
	m.ctx, m.stop = context.WithCancel(context.Background())
	return m
}

func (m *Manager) Stop() {
	m.stop()
}

func (m *Manager) GetOffset(ctx context.Context, subId string) (info.ListOffsetInfo, error) {
	subOffset, err := m.getSubscriptionOffset(ctx, subId)
	if err != nil {
		return nil, err
	}
	return subOffset.getOffsets(), nil
}

func (m *Manager) Offset(ctx context.Context, subInfo info.SubscriptionInfo) error {
	subOffset, err := m.getSubscriptionOffset(ctx, subInfo.SubId)
	if err != nil {
		return err
	}
	subOffset.offset(subInfo.Offsets)
	return nil
}

func (m *Manager) getSubscriptionOffset(ctx context.Context, subId string) (*subscriptionOffset, error) {
	subOffset, exist := m.subOffset.Load(subId)
	if !exist {
		sub, err := m.initSubscriptionOffset(ctx, subId)
		if err != nil {
			return nil, err
		}
		subOffset, _ = m.subOffset.LoadOrStore(subId, sub)
	}
	return subOffset.(*subscriptionOffset), nil
}

func (m *Manager) initSubscriptionOffset(ctx context.Context, subId string) (*subscriptionOffset, error) {
	list, err := m.storage.GetOffsets(ctx, subId)
	if err != nil {
		return nil, err
	}
	subOffset := &subscriptionOffset{
		subId: subId,
	}
	for _, o := range list {
		subOffset.offsets.Store(o.EventLog, &eventLogOffset{
			subId:      subId,
			eventLog:   o.EventLog,
			offset:     o.Offset,
			commit:     o.Offset,
			checkExist: true,
		})
	}
	return subOffset, nil

}

func (m *Manager) RemoveRegisterSubscription(ctx context.Context, subId string) error {
	m.subOffset.Delete(subId)
	return m.storage.DeleteOffset(ctx, subId)
}

func (m *Manager) Start() {
	go func() {
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-m.ctx.Done():
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				m.commit(ctx)
				cancel()
				return
			case <-ticker.C:
				m.commit(m.ctx)
			}
		}
	}()
}

func (m *Manager) commit(ctx context.Context) {
	var wg sync.WaitGroup
	m.subOffset.Range(func(key, value interface{}) bool {
		sub := value.(*subscriptionOffset)
		wg.Add(1)
		go func(sub *subscriptionOffset) {
			wg.Done()
			sub.commitOffset(ctx, m.storage)
		}(sub)
		return true
	})
	wg.Wait()
}

type subscriptionOffset struct {
	subId   string
	offsets sync.Map
}

func (o *subscriptionOffset) getSetEventLogOffset(info info.OffsetInfo) *eventLogOffset {
	elOffset, exist := o.offsets.Load(info.EventLog)
	if !exist {
		elOffset = &eventLogOffset{
			subId:    o.subId,
			eventLog: info.EventLog,
			offset:   info.Offset,
		}
		elOffset, _ = o.offsets.LoadOrStore(info.EventLog, elOffset)
	}
	return elOffset.(*eventLogOffset)
}

func (o *subscriptionOffset) offset(infos info.ListOffsetInfo) {
	for _, offset := range infos {
		elOffset := o.getSetEventLogOffset(offset)
		elOffset.setOffset(offset.Offset)
	}
}

func (o *subscriptionOffset) getOffsets() info.ListOffsetInfo {
	var offsets info.ListOffsetInfo
	o.offsets.Range(func(key, value interface{}) bool {
		elOffset := value.(*eventLogOffset)
		offsets = append(offsets, info.OffsetInfo{
			EventLog: elOffset.eventLog,
			Offset:   elOffset.offset,
		})
		return true
	})
	return offsets
}

func (o *subscriptionOffset) commitOffset(ctx context.Context, storage storage.OffsetStorage) {
	o.offsets.Range(func(key, value interface{}) bool {
		elOffset := value.(*eventLogOffset)
		err := elOffset.commitOffset(ctx, storage)
		if err != nil {
			log.Warning(ctx, "commit offset fail", map[string]interface{}{
				log.KeySubscriptionID: o.subId,
				log.KeyEventlogID:     elOffset.eventLog,
				"offset":              elOffset.offset,
				log.KeyError:          err,
			})
		}
		return true
	})
}

type eventLogOffset struct {
	subId      string
	eventLog   string
	offset     int64
	commit     int64
	checkExist bool
}

func (o *eventLogOffset) setOffset(offset int64) {
	o.offset = offset
}

func (o *eventLogOffset) commitOffset(ctx context.Context, storage storage.OffsetStorage) error {
	offset := o.offset
	if !o.checkExist {
		err := storage.CreateOffset(ctx, o.subId, info.OffsetInfo{
			EventLog: o.eventLog,
			Offset:   offset,
		})
		if err != nil {
			return err
		}
		log.Debug(ctx, "create offset", map[string]interface{}{
			log.KeySubscriptionID: o.subId,
			log.KeyEventlogID:     o.eventLog,
			"offset":              offset,
		})
		o.checkExist = true
		o.commit = offset
		return nil
	}
	if o.commit == offset {
		return nil
	}
	err := storage.UpdateOffset(ctx, o.subId, info.OffsetInfo{
		EventLog: o.eventLog,
		Offset:   offset,
	})
	if err != nil {
		return err
	}
	o.commit = offset
	return nil
}