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
package volume

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/golang/glog"
)

// ThinPoolWatcher maintains a cache of device name -> usage stats for a devicemapper thin-pool using thin_ls.
type ThinPoolWatcher struct {
	poolName       string
	metadataDevice string
	lock           *sync.RWMutex
	cache          map[string]uint64
	period         time.Duration
	stopChan       chan struct{}
}

// NewThinPoolWatcher returns a new ThinPoolWatcher for the given devicemapper thin pool name.
func NewThinPoolWatcher(poolName, metadataDevice string) *ThinPoolWatcher {
	return &ThinPoolWatcher{poolName, &sync.RWMutex{}, make(map[string]uint64), make(chan struct{})}
}

func (w *ThinPoolWatcher) Start() {
	w.Refresh()
	for {
		select {
		case <-w.stopChan:
			return
		case <-time.After(w.period):
			start := time.Now()
			w.Refresh()
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

// Refresh performs a `thin_ls` of the pool being watched and refreshes the
// cached data with the result.
func (w *ThinPoolWatcher) Refresh() {
	output, err := doThinLs(w.poolName, w.metadataDevice)
	if err != nil {
		glog.Errorf("unable to get usage for thin-pool %v: %v", w.poolName, err)
		return
	}

	w.lock.Lock()
	defer w.lock.Unlock()

	w.cache = parseThinLsOutput(output)
}

// doThinLs handles obtaining the output of thin_ls for the given pool name; it:
//
// 1. Reserves a metadata snapshot for the pool
// 2. Runs thin_ls against that snapshot
// 3. Releases the snapshot
func doThinLs(poolName, metadataDevice string) ([]byte, error) {
	// (1)
	// NOTE: "0" in the call below is for the 'sector' argument to 'dmsetup message'.  It's not needed for thin pools.
	if _, err := exec.Command("dmsetup", "message", poolName, "0", "reserve_metadata_snap").Output(); err != nil {
		return nil, fmt.Errorf("%v, %v", os.Stderr, err)
	}
	// (3)
	defer func() {
		exec.Command("dmsetup", "message", poolName, "0", "release_metadata_snap").Run()
	}()

	// (2)
	output, err := exec.Command("thin_ls", "--no-headers", "-m", "-o", "DEV,EXCLUSIVE_BYTES", metadataDevice).Output()
	if err != nil {
		return nil, fmt.Errorf("%v, %v", os.Stderr, err)
	}

	return output, nil
}

// parseThinLsOutput parses the output returned by thin_ls to build a map of device id -> usage.
func parseThinLsOutput(output []byte) map[string]uint64 {
	cache := map[string]uint64{}

	// parse output
	scanner := bufio.NewScanner(bytes.NewReader(output))
	for scanner.Scan() {
		output := scanner.Text()
		deviceID := strings.Fields(output)[0]
		usage, err := strconv.ParseUint(strings.Fields(output)[1], 10, 64)
		if err != nil {
			// parse error, log and continue
			continue
		}

		cache[deviceID] = usage
	}

	return cache
}
