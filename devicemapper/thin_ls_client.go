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
	"bufio"
	"bytes"
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	"github.com/golang/glog"
)

// thinLsClient knows how to run a thin_ls very specific to CoW usage for containers.
type thinLsClient interface {
	ThinLs(deviceName string) (map[string]uint64, error)
}

func newThinLsClient() thinLsClient {
	return &defaultThinLsClient{}
}

type defaultThinLsClient struct{}

var _ thinLsClient = &defaultThinLsClient{}

func (*defaultThinLsClient) ThinLs(deviceName string) (map[string]uint64, error) {
	args := []string{"--no-headers", "-m", "-o", "DEV,EXCLUSIVE_BYTES", deviceName}
	glog.V(4).Infof("running command: thin_ls %v", strings.Join(args, " "))

	output, err := exec.Command("thin_ls", args...).Output()
	if err != nil {
		return nil, fmt.Errorf("Error running command `thin_ls %v`: %v\noutput:\n\n%v", strings.Join(args, " "), err, string(output))
	}

	return parseThinLsOutput(output), nil
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
