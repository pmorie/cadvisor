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
	"strconv"
)

// DmsetupClient is a low-level client for interacting with devicemapper
type DmsetupClient interface {
	Table(deviceName string) ([]byte, error)
	Message(deviceName string, sector int, message string) ([]byte, error)
	Status(deviceName string) ([]byte, error)
}

func NewDmsetupClient() DmsetupClient {
	return &defaultDmsetupClient{}
}

// defaultDmsetupClient implements the standard behavior for interacting with dmsetup.
type defaultDmsetupClient struct{}

var _ DmsetupClient = &defaultDmsetupClient{}

func (*defaultDmsetupClient) Table(deviceName string) ([]byte, error) {
	return exec.Command("dmsetup", "table", deviceName).Output()
}

func (*defaultDmsetupClient) Message(deviceName string, sector int, message string) ([]byte, error) {
	return exec.Command("dmsetup", "message", deviceName, strconv.Itoa(sector), message).Output()
}

func (*defaultDmsetupClient) Status(deviceName string) ([]byte, error) {
	return exec.Command("dmsetup", "status", deviceName).Output()
}
