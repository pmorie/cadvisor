// Copyright 2014 Google Inc. All Rights Reserved.
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

// Handler for Docker containers.
package docker

import (
	"fmt"
	"io/ioutil"
	"path"
	"strings"
	"time"

	"github.com/google/cadvisor/container"
	"github.com/google/cadvisor/container/common"
	containerlibcontainer "github.com/google/cadvisor/container/libcontainer"
	"github.com/google/cadvisor/devicemapper"
	"github.com/google/cadvisor/fs"
	info "github.com/google/cadvisor/info/v1"
	dockerutil "github.com/google/cadvisor/utils/docker"

	docker "github.com/docker/engine-api/client"
	dockertypes "github.com/docker/engine-api/types"
	dockercontainer "github.com/docker/engine-api/types/container"
	"github.com/opencontainers/runc/libcontainer/cgroups"
	cgroupfs "github.com/opencontainers/runc/libcontainer/cgroups/fs"
	libcontainerconfigs "github.com/opencontainers/runc/libcontainer/configs"
	"golang.org/x/net/context"
)

const (
	// The read write layers exist here.
	aufsRWLayer = "diff"
	// Path to the directory where docker stores log files if the json logging driver is enabled.
	pathToContainersDir = "containers"
)

type dockerContainerHandler struct {
	client             *docker.Client
	name               string
	id                 string
	aliases            []string
	machineInfoFactory info.MachineInfoFactory

	// Absolute path to the cgroup hierarchies of this container.
	// (e.g.: "cpu" -> "/sys/fs/cgroup/cpu/test")
	cgroupPaths map[string]string

	// Manager of this container's cgroups.
	cgroupManager cgroups.Manager

	// the docker storage driver
	storageDriver    storageDriver
	fsInfo           fs.FsInfo
	rootfsStorageDir string

	// devicemapper state

	// the devicemapper poolname
	poolName string
	// the devicemapper device id for the container
	deviceID string

	// Time at which this container was created.
	creationTime time.Time

	// Metadata associated with the container.
	labels map[string]string
	envs   map[string]string

	// The container PID used to switch namespaces as required
	pid int

	// Image name used for this container.
	image string

	// The host root FS to read
	rootFs string

	// The network mode of the container
	networkMode dockercontainer.NetworkMode

	// Filesystem handler.
	fsHandler common.FsHandler

	ignoreMetrics container.MetricSet

	// thin pool watcher
	thinPoolWatcher *devicemapper.ThinPoolWatcher
}

var _ container.ContainerHandler = &dockerContainerHandler{}

func getRwLayerID(containerID, storageDir string, sd storageDriver, dockerVersion []int) (string, error) {
	const (
		// Docker version >=1.10.0 have a randomized ID for the root fs of a container.
		randomizedRWLayerMinorVersion = 10
		rwLayerIDFile                 = "mount-id"
	)
	if (dockerVersion[0] <= 1) && (dockerVersion[1] < randomizedRWLayerMinorVersion) {
		return containerID, nil
	}

	bytes, err := ioutil.ReadFile(path.Join(storageDir, "image", string(sd), "layerdb", "mounts", containerID, rwLayerIDFile))
	if err != nil {
		return "", fmt.Errorf("failed to identify the read-write layer ID for container %q. - %v", containerID, err)
	}
	return string(bytes), err
}

// newDockerContainerHandler returns a new container.ContainerHandler
func newDockerContainerHandler(
	client *docker.Client,
	name string,
	machineInfoFactory info.MachineInfoFactory,
	fsInfo fs.FsInfo,
	storageDriver storageDriver,
	storageDir string,
	cgroupSubsystems *containerlibcontainer.CgroupSubsystems,
	inHostNamespace bool,
	metadataEnvs []string,
	dockerVersion []int,
	ignoreMetrics container.MetricSet,
	thinPoolWatcher *devicemapper.ThinPoolWatcher,
) (container.ContainerHandler, error) {
	// Create the cgroup paths.
	cgroupPaths := make(map[string]string, len(cgroupSubsystems.MountPoints))
	for key, val := range cgroupSubsystems.MountPoints {
		cgroupPaths[key] = path.Join(val, name)
	}

	// Generate the equivalent cgroup manager for this container.
	cgroupManager := &cgroupfs.Manager{
		Cgroups: &libcontainerconfigs.Cgroup{
			Name: name,
		},
		Paths: cgroupPaths,
	}

	rootFs := "/"
	if !inHostNamespace {
		rootFs = "/rootfs"
		storageDir = path.Join(rootFs, storageDir)
	}

	id := ContainerNameToDockerId(name)

	// Add the Containers dir where the log files are stored.
	// FIXME: Give `otherStorageDir` a more descriptive name.
	otherStorageDir := path.Join(storageDir, pathToContainersDir, id)

	rwLayerID, err := getRwLayerID(id, storageDir, storageDriver, dockerVersion)
	if err != nil {
		return nil, err
	}

	// Determine the rootfs storage dir OR the pool name to determine the device
	var (
		rootfsStorageDir string
		poolName         string
	)
	switch storageDriver {
	case aufsStorageDriver:
		rootfsStorageDir = path.Join(storageDir, string(aufsStorageDriver), aufsRWLayer, rwLayerID)
	case overlayStorageDriver:
		rootfsStorageDir = path.Join(storageDir, string(overlayStorageDriver), rwLayerID)
	case devicemapperStorageDriver:
		dockerInfo, err := DockerInfo()
		if err != nil {
			return nil, err
		}

		poolName, err = dockerutil.DockerThinPoolName(dockerInfo)
		if err != nil {
			return nil, err
		}
	}

	// TODO: extract object mother method
	handler := &dockerContainerHandler{
		id:                 id,
		client:             client,
		name:               name,
		machineInfoFactory: machineInfoFactory,
		cgroupPaths:        cgroupPaths,
		cgroupManager:      cgroupManager,
		storageDriver:      storageDriver,
		fsInfo:             fsInfo,
		rootFs:             rootFs,
		poolName:           poolName,
		rootfsStorageDir:   rootfsStorageDir,
		envs:               make(map[string]string),
		ignoreMetrics:      ignoreMetrics,
		thinPoolWatcher:    thinPoolWatcher,
	}

	// We assume that if Inspect fails then the container is not known to docker.
	ctnr, err := client.ContainerInspect(context.Background(), id)
	if err != nil {
		return nil, fmt.Errorf("failed to inspect container %q: %v", id, err)
	}
	// Timestamp returned by Docker is in time.RFC3339Nano format.
	handler.creationTime, err = time.Parse(time.RFC3339Nano, ctnr.Created)
	if err != nil {
		// This should not happen, report the error just in case
		return nil, fmt.Errorf("failed to parse the create timestamp %q for container %q: %v", ctnr.Created, id, err)
	}
	handler.pid = ctnr.State.Pid

	// Add the name and bare ID as aliases of the container.
	handler.aliases = append(handler.aliases, strings.TrimPrefix(ctnr.Name, "/"), id)
	handler.labels = ctnr.Config.Labels
	handler.image = ctnr.Config.Image
	handler.networkMode = ctnr.HostConfig.NetworkMode
	handler.deviceID = ctnr.GraphDriver.Data["DeviceId"]

	if !ignoreMetrics.Has(container.DiskUsageMetrics) {
		handler.fsHandler = &dockerFsHandler{
			fsHandler:       common.NewFsHandler(time.Minute, rootfsStorageDir, otherStorageDir, fsInfo),
			thinPoolWatcher: thinPoolWatcher,
			deviceID:        handler.deviceID,
		}
	}

	// split env vars to get metadata map.
	for _, exposedEnv := range metadataEnvs {
		for _, envVar := range ctnr.Config.Env {
			splits := strings.SplitN(envVar, "=", 2)
			if splits[0] == exposedEnv {
				handler.envs[strings.ToLower(exposedEnv)] = splits[1]
			}
		}
	}

	return handler, nil
}

// dockerFsHandler is a composite FsHandler implementation the incorporates
// the common fs handler and a devicemapper ThinPoolWatcher.
type dockerFsHandler struct {
	fsHandler common.FsHandler

	// thinPoolWatcher is the devicemapper thin pool watcher
	thinPoolWatcher *devicemapper.ThinPoolWatcher
	// deviceID is the id of the container's fs device
	deviceID string
}

var _ common.FsHandler = &dockerFsHandler{}

func (h *dockerFsHandler) Start() {
	h.fsHandler.Start()
}

func (h *dockerFsHandler) Stop() {
	h.fsHandler.Stop()
}

func (h *dockerFsHandler) Usage() (uint64, uint64) {
	baseUsage, usage := h.fsHandler.Usage()

	// When devicemapper is the storage driver, the base usage of the container comes from the thin pool.
	// We still need the result of the fsHandler for any extra storage associated with the container.
	// To correctly factor in the thin pool usage, we should:
	// * Usage the thin pool usage as the base usage
	// * Calculate the overall usage by adding the overall usage from the fs handler to the thin pool usage
	if h.thinPoolWatcher != nil {
		thinPoolUsage, err := h.thinPoolWatcher.GetUsage(h.deviceID)
		if err != nil {
			// log it.
		}

		baseUsage = thinPoolUsage
		usage += thinPoolUsage
	}

	return baseUsage, usage
}

func (self *dockerContainerHandler) Start() {
	if self.fsHandler != nil {
		self.fsHandler.Start()
	}
}

func (self *dockerContainerHandler) Cleanup() {
	if self.fsHandler != nil {
		self.fsHandler.Stop()
	}
}

func (self *dockerContainerHandler) ContainerReference() (info.ContainerReference, error) {
	return info.ContainerReference{
		Id:        self.id,
		Name:      self.name,
		Aliases:   self.aliases,
		Namespace: DockerNamespace,
		Labels:    self.labels,
	}, nil
}

func (self *dockerContainerHandler) needNet() bool {
	if !self.ignoreMetrics.Has(container.NetworkUsageMetrics) {
		return !self.networkMode.IsContainer()
	}
	return false
}

func (self *dockerContainerHandler) GetSpec() (info.ContainerSpec, error) {
	hasFilesystem := !self.ignoreMetrics.Has(container.DiskUsageMetrics)
	spec, err := common.GetSpec(self.cgroupPaths, self.machineInfoFactory, self.needNet(), hasFilesystem)

	spec.Labels = self.labels
	spec.Envs = self.envs
	spec.Image = self.image

	return spec, err
}

func (self *dockerContainerHandler) getFsStats(stats *info.ContainerStats) error {
	if self.ignoreMetrics.Has(container.DiskUsageMetrics) {
		return nil
	}
	var device string
	switch self.storageDriver {
	case devicemapperStorageDriver:
		// Device has to be the pool name to correlate with the device name as
		// set in the machine info filesystems.
		device = self.poolName
	case aufsStorageDriver, overlayStorageDriver, zfsStorageDriver:
		deviceInfo, err := self.fsInfo.GetDirFsDevice(self.rootfsStorageDir)
		if err != nil {
			return err
		}
		device = deviceInfo.Device
	default:
		return nil
	}

	mi, err := self.machineInfoFactory.GetMachineInfo()
	if err != nil {
		return err
	}

	var (
		limit  uint64
		fsType string
	)

	// Docker does not impose any filesystem limits for containers. So use capacity as limit.
	for _, fs := range mi.Filesystems {
		if fs.Device == device {
			limit = fs.Capacity
			fsType = fs.Type
			break
		}
	}

	fsStat := info.FsStats{Device: device, Type: fsType, Limit: limit}
	fsStat.BaseUsage, fsStat.Usage = self.fsHandler.Usage()

	stats.Filesystem = append(stats.Filesystem, fsStat)

	return nil
}

// TODO(vmarmol): Get from libcontainer API instead of cgroup manager when we don't have to support older Dockers.
func (self *dockerContainerHandler) GetStats() (*info.ContainerStats, error) {
	stats, err := containerlibcontainer.GetStats(self.cgroupManager, self.rootFs, self.pid, self.ignoreMetrics)
	if err != nil {
		return stats, err
	}
	// Clean up stats for containers that don't have their own network - this
	// includes containers running in Kubernetes pods that use the network of the
	// infrastructure container. This stops metrics being reported multiple times
	// for each container in a pod.
	if !self.needNet() {
		stats.Network = info.NetworkStats{}
	}

	// Get filesystem stats.
	err = self.getFsStats(stats)
	if err != nil {
		return stats, err
	}

	return stats, nil
}

func (self *dockerContainerHandler) ListContainers(listType container.ListType) ([]info.ContainerReference, error) {
	// No-op for Docker driver.
	return []info.ContainerReference{}, nil
}

func (self *dockerContainerHandler) GetCgroupPath(resource string) (string, error) {
	path, ok := self.cgroupPaths[resource]
	if !ok {
		return "", fmt.Errorf("could not find path for resource %q for container %q\n", resource, self.name)
	}
	return path, nil
}

func (self *dockerContainerHandler) ListThreads(listType container.ListType) ([]int, error) {
	// TODO(vmarmol): Implement.
	return nil, nil
}

func (self *dockerContainerHandler) GetContainerLabels() map[string]string {
	return self.labels
}

func (self *dockerContainerHandler) ListProcesses(listType container.ListType) ([]int, error) {
	return containerlibcontainer.GetProcesses(self.cgroupManager)
}

func (self *dockerContainerHandler) WatchSubcontainers(events chan container.SubcontainerEvent) error {
	return fmt.Errorf("watch is unimplemented in the Docker container driver")
}

func (self *dockerContainerHandler) StopWatchingSubcontainers() error {
	// No-op for Docker driver.
	return nil
}

func (self *dockerContainerHandler) Exists() bool {
	return common.CgroupExists(self.cgroupPaths)
}

func DockerInfo() (dockertypes.Info, error) {
	client, err := Client()
	if err != nil {
		return dockertypes.Info{}, fmt.Errorf("unable to communicate with docker daemon: %v", err)
	}
	return client.Info(context.Background())
}

func DockerImages() ([]dockertypes.Image, error) {
	client, err := Client()
	if err != nil {
		return nil, fmt.Errorf("unable to communicate with docker daemon: %v", err)
	}
	return client.ImageList(context.Background(), dockertypes.ImageListOptions{All: false})
}

// Checks whether the dockerInfo reflects a valid docker setup, and returns it if it does, or an
// error otherwise.
func ValidateInfo() (*dockertypes.Info, error) {
	client, err := Client()
	if err != nil {
		return nil, fmt.Errorf("unable to communicate with docker daemon: %v", err)
	}

	dockerInfo, err := client.Info(context.Background())
	if err != nil {
		return nil, fmt.Errorf("failed to detect Docker info: %v", err)
	}

	// Fall back to version API if ServerVersion is not set in info.
	if dockerInfo.ServerVersion == "" {
		version, err := client.ServerVersion(context.Background())
		if err != nil {
			return nil, fmt.Errorf("unable to get docker version: %v", err)
		}
		dockerInfo.ServerVersion = version.Version
	}
	version, err := parseDockerVersion(dockerInfo.ServerVersion)
	if err != nil {
		return nil, err
	}

	if version[0] < 1 {
		return nil, fmt.Errorf("cAdvisor requires docker version %v or above but we have found version %v reported as %q", []int{1, 0, 0}, version, dockerInfo.ServerVersion)
	}

	// Check that the libcontainer execdriver is used if the version is < 1.11
	// (execution drivers are no longer supported as of 1.11).
	if version[0] <= 1 && version[1] <= 10 &&
		!strings.HasPrefix(dockerInfo.ExecutionDriver, "native") {
		return nil, fmt.Errorf("docker found, but not using native exec driver")
	}

	if dockerInfo.Driver == "" {
		return nil, fmt.Errorf("failed to find docker storage driver")
	}

	return &dockerInfo, nil
}
