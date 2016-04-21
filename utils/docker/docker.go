package docker

import (
	"fmt"
	"strings"

	"github.com/fsouza/go-dockerclient"
)

const (
	DockerInfoDriver         = "Driver"
	DockerInfoDriverStatus   = "DriverStatus"
	DriverStatusPoolName     = "Pool Name"
	DriverStatusDataLoopFile = "Data loop file"
)

func DriverStatusValue(status [][2]string, target string) string {
	for _, v := range status {
		if strings.ToLower(v[0]) == strings.ToLower(target) {
			return v[1]
		}
	}
	return ""
}

func DockerThinPoolName(dockerInfo docker.DockerInfo) (string, error) {
	poolName := DriverStatusValue(dockerInfo.DriverStatus, DriverStatusPoolName)
	if len(poolName) == 0 {
		return "", fmt.Errorf("Could not get devicemapper pool name")
	}

	return poolName, nil
}
