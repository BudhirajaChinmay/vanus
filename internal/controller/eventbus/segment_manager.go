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

package eventbus

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/huandu/skiplist"
	"github.com/linkall-labs/vanus/internal/controller/eventbus/info"
	"github.com/linkall-labs/vanus/internal/controller/eventbus/selector"
	"github.com/linkall-labs/vanus/internal/kv"
	"github.com/linkall-labs/vanus/observability/log"
	ctrlpb "github.com/linkall-labs/vsproto/pkg/controller"
	"github.com/linkall-labs/vsproto/pkg/segment"
	"github.com/pkg/errors"
	"strings"
	"sync"
	"time"
)

const (
	defaultSegmentBlockSize = 64 * 1024 * 1024
)

var (
	ErrEventLogNotFound = errors.New("eventlog not found")
)

type segmentPool struct {
	ctrl                     *controller
	selectorForSegmentCreate selector.SegmentServerSelector
	eventLogSegment          map[string]*skiplist.SkipList
	segmentMap               sync.Map
	kvStore                  kv.Client
}

func newSegmentPool(ctrl *controller) *segmentPool {
	return &segmentPool{
		ctrl:            ctrl,
		eventLogSegment: map[string]*skiplist.SkipList{},
		kvStore:         ctrl.kvStore,
	}
}

func (pool *segmentPool) init(ctx context.Context) error {
	go pool.dynamicAllocateSegmentTask()
	pool.selectorForSegmentCreate = selector.NewSegmentServerRoundRobinSelector(pool.ctrl.ssMgr.getServerInfos)
	pairs, err := pool.kvStore.List(ctx, segmentBlockKeyPrefixInKVStore)
	if err != nil {
		return err
	}
	// TODO unassigned -> assigned
	for idx := range pairs {
		pair := pairs[idx]
		sbInfo := &info.SegmentBlockInfo{}
		err := json.Unmarshal(pair.Value, sbInfo)
		if err != nil {
			return err
		}
		l, exist := pool.eventLogSegment[sbInfo.EventLogID]
		if !exist {
			l = skiplist.New(skiplist.String)
			pool.eventLogSegment[sbInfo.EventLogID] = l
		}
		l.Set(sbInfo.ID, sbInfo)
		pool.segmentMap.Store(sbInfo.ID, sbInfo)
	}
	return nil
}

func (pool *segmentPool) destroy() error {
	return nil
}

func (pool *segmentPool) bindSegment(ctx context.Context, el *info.EventLogInfo, num int) ([]*info.SegmentBlockInfo, error) {
	segArr := make([]*info.SegmentBlockInfo, num+1)
	var err error
	defer func() {
		if err == nil {
			return
		}
		for idx := 0; idx < num; idx++ {
			if idx == 0 && segArr[idx] != nil {
				segArr[idx].NextSegmentId = ""
			} else {
				segArr[idx].EventLogID = ""
				segArr[idx].ReplicaGroupID = ""
				segArr[idx].PeersAddress = nil
			}
		}
		for idx := 0; idx < num; idx++ {
			if segArr[idx] != nil {
				if _err := pool.updateSegmentBlockInKV(ctx, segArr[idx]); _err != nil {
					log.Error(ctx, "update segment info in kv store failed when cancel binding", map[string]interface{}{
						log.KeyError: _err,
					})
				}
			}
		}
	}()

	segArr[0] = pool.getLastSegmentOfEventLog(el)
	for idx := 1; idx < len(segArr); idx++ {
		seg, err := pool.pickSegment(ctx, defaultSegmentBlockSize)
		if err != nil {
			return nil, err
		}

		// binding, assign runtime fields
		seg.EventLogID = el.ID
		if err = pool.createSegmentBlockReplicaGroup(seg); err != nil {
			return nil, err
		}

		srvInfo := pool.ctrl.ssMgr.GetServerInfoByServerID(seg.VolumeInfo.SegmentServerID)
		client := pool.ctrl.ssMgr.getSegmentServerClient(srvInfo)
		if client == nil {
			return nil, errors.New("the segment server client not found")
		}
		_, err = client.ActiveSegmentBlock(ctx, &segment.ActiveSegmentBlockRequest{
			EventLogId:     seg.EventLogID,
			ReplicaGroupId: seg.ReplicaGroupID,
			PeersAddress:   seg.PeersAddress,
		})
		if err != nil {
			return nil, err
		}
		if segArr[idx-1] != nil {
			segArr[idx-1].NextSegmentId = seg.ID
			seg.PreviousSegmentId = segArr[idx-1].ID
		}
		segArr[idx] = seg
	}

	sl, exist := pool.eventLogSegment[el.ID]
	if !exist {
		sl = skiplist.New(skiplist.String)
		pool.eventLogSegment[el.ID] = sl
	}
	for idx := range segArr {
		if segArr[idx] == nil {
			continue
		}
		sl.Set(segArr[idx].ID, segArr[idx])
		if err = pool.updateSegmentBlockInKV(ctx, segArr[idx]); err != nil {
			return nil, err
		}
	}
	return segArr, nil
}

func (pool *segmentPool) pickSegment(ctx context.Context, size int64) (*info.SegmentBlockInfo, error) {
	// no enough segment, manually allocate and bind
	return pool.allocateSegmentImmediately(ctx, defaultSegmentBlockSize)
}

func (pool *segmentPool) allocateSegmentImmediately(ctx context.Context, size int64) (*info.SegmentBlockInfo, error) {
	srvInfo := pool.selectorForSegmentCreate.Select(ctx, size)
	client := pool.ctrl.ssMgr.getSegmentServerClient(srvInfo)
	volume := pool.ctrl.volumeMgr.lookupVolumeByServerID(srvInfo.ID())
	segmentInfo := &info.SegmentBlockInfo{
		ID:         pool.generateSegmentBlockID(),
		Capacity:   size,
		VolumeID:   volume.ID(),
		VolumeInfo: volume,
	}
	_, err := client.CreateSegmentBlock(ctx, &segment.CreateSegmentBlockRequest{
		Size: segmentInfo.Capacity,
		Id:   segmentInfo.ID,
	})
	if err != nil {
		return nil, err
	}
	volume.AddBlock(segmentInfo)
	if err = pool.ctrl.volumeMgr.updateVolumeInKV(ctx, volume); err != nil {
		return nil, err
	}

	pool.segmentMap.Store(segmentInfo.ID, segmentInfo)
	if err = pool.updateSegmentBlockInKV(ctx, segmentInfo); err != nil {
		return nil, err
	}
	return segmentInfo, nil
}

func (pool *segmentPool) dynamicAllocateSegmentTask() {
	//TODO
}

func (pool *segmentPool) generateSegmentBlockID() string {
	//TODO optimize
	return fmt.Sprintf("%d", time.Now().UnixNano())
}

func (pool *segmentPool) createSegmentBlockReplicaGroup(segInfo *info.SegmentBlockInfo) error {
	// TODO implement
	segInfo.ReplicaGroupID = "group-1"
	segInfo.PeersAddress = []string{"ip1", "ip2"}
	// pick 2 segments with same capacity
	return nil
}

func (pool *segmentPool) getAppendableSegment(ctx context.Context,
	eli *info.EventLogInfo, num int) ([]*info.SegmentBlockInfo, error) {
	sl, exist := pool.eventLogSegment[eli.ID]
	if !exist {
		return nil, ErrEventLogNotFound
	}
	arr := make([]*info.SegmentBlockInfo, 0)
	next := sl.Front()
	hit := 0
	for hit < num && next != nil {
		sbi := next.Value.(*info.SegmentBlockInfo)
		next = next.Next()
		if sbi.IsFull {
			continue
		}
		hit++
		arr = append(arr, sbi)
	}

	if len(arr) == 0 {
		return pool.bindSegment(ctx, eli, 1)
	}
	return arr, nil
}

func (pool *segmentPool) updateSegment(ctx context.Context, req *ctrlpb.SegmentHeartbeatRequest) error {
	for idx := range req.HealthInfo {
		hInfo := req.HealthInfo[idx]

		// TODO there is problem in data structure design OPTIMIZE
		v, exist := pool.segmentMap.Load(hInfo.Id)
		if !exist {
			log.Warning(ctx, "the segment not found when heartbeat", map[string]interface{}{
				"segment_id": hInfo.Id,
			})
			continue
		}
		in := v.(*info.SegmentBlockInfo)
		if hInfo.IsFull {
			in.IsFull = true

			next := pool.getSegmentBlockByID(ctx, in.NextSegmentId)
			if next != nil {
				next.StartOffsetInLog = in.StartOffsetInLog + int64(in.Number)
				if err := pool.updateSegmentBlockInKV(ctx, next); err != nil {
					log.Warning(ctx, "update the segment's start_offset failed ", map[string]interface{}{
						"segment_id":   hInfo.Id,
						"next_segment": next.ID,
						log.KeyError:   err,
					})
					return err
				}
			}
		}
		in.Size = hInfo.Size
		in.Number = hInfo.EventNumber
		if err := pool.updateSegmentBlockInKV(ctx, in); err != nil {
			log.Warning(ctx, "update the segment failed ", map[string]interface{}{
				"segment_id": hInfo.Id,
				log.KeyError: err,
			})
			return err
		}
	}
	return nil
}

func (pool *segmentPool) getEventLogSegmentList(elID string) []*info.SegmentBlockInfo {
	el, exist := pool.eventLogSegment[elID]
	if !exist {
		return nil
	}
	var arr []*info.SegmentBlockInfo
	next := el.Front()
	for next != nil {
		arr = append(arr, next.Value.(*info.SegmentBlockInfo))
		next = next.Next()
	}
	return arr
}

func (pool *segmentPool) getSegmentBlockByID(ctx context.Context, id string) *info.SegmentBlockInfo {
	v, exist := pool.segmentMap.Load(id)
	if !exist {
		return nil
	}

	return v.(*info.SegmentBlockInfo)
}

func (pool *segmentPool) getSegmentBlockKeyInKVStore(sbID string) string {
	return strings.Join([]string{segmentBlockKeyPrefixInKVStore, sbID}, "/")
}

func (pool *segmentPool) updateSegmentBlockInKV(ctx context.Context, segment *info.SegmentBlockInfo) error {
	if segment == nil {
		return nil
	}
	data, err := json.Marshal(segment)
	if err != nil {
		return err
	}
	return pool.kvStore.Set(ctx, pool.getSegmentBlockKeyInKVStore(segment.ID), data)
}

func (pool *segmentPool) getLastSegmentOfEventLog(el *info.EventLogInfo) *info.SegmentBlockInfo {
	sl := pool.eventLogSegment[el.ID]
	if sl == nil {
		return nil
	}
	val := sl.Back().Value
	if val == nil {
		return nil
	}
	return val.(*info.SegmentBlockInfo)
}