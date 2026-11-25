package bootstrap

import (
	"fmt"
	"strings"

	"rondo/internal/config"
	"rondo/internal/kobj"
)

func PlatformInstallFor(c *config.Config, version string) kobj.PlatformInstall {
	prefix := c.ArtifactoryHost() + "/" + c.Artifactory.DockerRepository
	return kobj.PlatformInstall{
		ControllerImage:   fmt.Sprintf("%s/rondo-controller:%s", prefix, version),
		ToolboxImage:      fmt.Sprintf("%s/rondo-toolbox:%s", prefix, version),
		TofuStateBaseURL:  c.StateBaseURL(),
		DashboardHostname: c.DashboardHostname(),
		ArtifactoryURL:    strings.TrimRight(c.Artifactory.URL, "/"),
		VMModuleURL: fmt.Sprintf("%s/%s/tofu-modules/vm/%s/vm.tar.gz",
			strings.TrimRight(c.Artifactory.URL, "/"), c.Artifactory.GenericRepository, version),
		AnsibleContentURL: fmt.Sprintf("%s/%s/ansible/rondo/%s/rondo-ansible.tar.gz",
			strings.TrimRight(c.Artifactory.URL, "/"), c.Artifactory.GenericRepository, version),
	}
}
