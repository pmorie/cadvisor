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
	"os/exec"
)

// thinLsClient knows how to run a thin_ls very specific to CoW usage for containers.
type thinLsClient interface {
	ThinLs(deviceName string) ([]byte, error)
}

func newThinLsClient() thinLsClient {
	return &defaultThinLsClient{}
}

type defaultThinLsClient struct{}

var _ thinLsClient = &defaultThinLsClient{}

func (*defaultThinLsClient) ThinLs(deviceName string) ([]byte, error) {
	return exec.Command("thin_ls", "--no-headers", "-m", "-o", "DEV,EXCLUSIVE_BYTES", deviceName).Output()
}
