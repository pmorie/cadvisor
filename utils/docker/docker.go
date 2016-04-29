package docker

import (
	"fmt"
	"strings"

	docker "github.com/docker/engine-api/client"
	dockertypes "github.com/docker/engine-api/client"
)

const (
	DockerInfoDriver         = "Driver"
	DockerInfoDriverStatus   = "DriverStatus"
	DriverStatusPoolName     = "Pool Name"
	DriverStatusDataLoopFile = "Data loop file"
	DriverStatusMetadataFile = "Metadata file"
)

func DriverStatusValue(status [][2]string, target string) string {
	for _, v := range status {
		if strings.ToLower(v[0]) == strings.ToLower(target) {
			return v[1]
		}
	}
	return ""
}

func DockerThinPoolName(info dockertypes.Info) (string, error) {
	poolName := DriverStatusValue(info.DriverStatus, DriverStatusPoolName)
	if len(poolName) == 0 {
		return "", fmt.Errorf("Could not get devicemapper pool name")
	}

	return poolName, nil
}

func DockerMetadataDevice(info dockertypes.Info) (string, error) {
	metadataDevice := DriverStatusValue(info.DriverStatus, DriverStatusMetadataFile)
	if len(metadataDevice) == 0 {
		return "", fmt.Errorf("Could not get the devicemapper metadata device")
	}

	return metadataDevice, nil
}
