// Copyright 2016 Google Inc. All Rights Reserved.
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
package devicemapper

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/golang/glog"
)

const (
	reserveMetadataMessage = "reserve_metadata_snap"
	releaseMetadataMessage = "release_metadata_snap"
)

// ThinPoolWatcher maintains a cache of device name -> usage stats for a devicemapper thin-pool using thin_ls.
type ThinPoolWatcher struct {
	poolName       string
	metadataDevice string
	lock           *sync.RWMutex
	cache          map[string]uint64
	period         time.Duration
	stopChan       chan struct{}
	dmsetup        DmsetupClient
	thinLsClient   thinLsClient
}

// NewThinPoolWatcher returns a new ThinPoolWatcher for the given devicemapper thin pool name and metadata device.
func NewThinPoolWatcher(poolName, metadataDevice string) *ThinPoolWatcher {
	return &ThinPoolWatcher{poolName: poolName,
		metadataDevice: metadataDevice,
		lock:           &sync.RWMutex{},
		cache:          make(map[string]uint64),
		period:         15 * time.Second,
		stopChan:       make(chan struct{}),
		dmsetup:        NewDmsetupClient(),
		thinLsClient:   newThinLsClient(),
	}
}

// Start starts the thin pool watcher.
func (w *ThinPoolWatcher) Start() {
	w.Refresh()
	for {
		select {
		case <-w.stopChan:
			return
		case <-time.After(w.period):
			// start := time.Now()
			w.Refresh()
			// print latency for refresh
		}
	}
}

func (w *ThinPoolWatcher) Stop() {
	close(w.stopChan)
}

// GetUsage gets the cached usage value of the given device.
func (w *ThinPoolWatcher) GetUsage(deviceId string) (uint64, error) {
	w.lock.RLock()
	defer w.lock.RUnlock()
	v, ok := w.cache[deviceId]
	if !ok {
		return 0, fmt.Errorf("no cached value for usage of device %v", deviceId)
	}

	return v, nil
}

const (
	thinPoolDmsetupStatusTokens           = 11
	thinPoolDmsetupStatusHeldMetadataRoot = 6
)

// Refresh performs a `thin_ls` of the pool being watched and refreshes the
// cached data with the result.
func (w *ThinPoolWatcher) Refresh() {
	w.lock.Lock()
	defer w.lock.Unlock()

	newCache, err := w.doThinLs(w.poolName, w.metadataDevice)
	if err != nil {
		glog.Errorf("unable to get usage for thin-pool %v: '%v'", w.poolName, err)
		return
	}

	w.cache = newCache
}

// doThinLs handles obtaining the output of thin_ls for the given pool name; it:
func (w *ThinPoolWatcher) doThinLs(poolName, metadataDevice string) (map[string]uint64, error) {
	currentlyReserved, err := w.checkReservation(poolName)
	if err != nil {
		glog.Errorf("error determining whether snapshot is reserved: %v", err)
	}

	if currentlyReserved {
		glog.V(4).Infof("metadata for %v is currently reserved; releasing", w.poolName)
		_, err = w.dmsetup.Message(w.poolName, 0, releaseMetadataMessage)
		if err != nil {
			glog.Errorf("error releasing metadata snapshot for %v: %v", w.poolName, err)
		}
	}

	glog.Infof("reserving metadata snapshot for thin-pool %v", poolName)
	// NOTE: "0" in the call below is for the 'sector' argument to 'dmsetup message'.  It's not needed for thin pools.
	if output, err := w.dmsetup.Message(poolName, 0, reserveMetadataMessage); err != nil {
		return nil, fmt.Errorf("error reserving metadata for thin-pool %v: %v output: %v", poolName, err, string(output))
	} else {
		glog.V(5).Infof("reserved metadata snapshot for thin-pool %v", poolName)
	}
	defer func() {
		glog.V(5).Infof("releasing metadata snapshot for thin-pool %v", poolName)
		w.dmsetup.Message(poolName, 0, releaseMetadataMessage)
	}()

	glog.V(5).Infof("running thin_ls on metadata device %v", metadataDevice)
	result, err := w.thinLsClient.ThinLs(metadataDevice)
	if err != nil {
		return nil, fmt.Errorf("error performing thin_ls on metadata device %v: %v", metadataDevice, err)
	}

	return result, nil
}

// checkReservation checks to see whether the thin device is currently holding userspace metadata.
func (w *ThinPoolWatcher) checkReservation(poolName string) (bool, error) {
	glog.V(5).Infof("checking whether the thin-pool is holding a metadata snapshot")
	output, err := w.dmsetup.Status(poolName)
	if err != nil {
		return false, err
	}

	tokens := strings.Split(string(output), " ")
	// Split returns the input as the last item in the result, adjust the number of tokens by one
	if len(tokens) != thinPoolDmsetupStatusTokens+1 {
		return false, fmt.Errorf("unexpected output of dmsetup status command; expected 11 fields, got %v; output: ", len(tokens), string(output))
	}

	heldMetadataRoot := tokens[thinPoolDmsetupStatusHeldMetadataRoot]
	currentlyReserved := heldMetadataRoot != "-"
	return currentlyReserved, nil
}
