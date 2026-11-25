package bootstrap

import (
	"fmt"

	"rondo/internal/config"
	"rondo/internal/kobj"
)

func PlatformInstallFor(c *config.Config, version string) kobj.PlatformInstall {
	m := c.Mirror()
	return kobj.PlatformInstall{
		ControllerImage:   fmt.Sprintf("%s/rondo-controller:%s", m.OCIRepository, version),
		ToolboxImage:      fmt.Sprintf("%s/rondo-toolbox:%s", m.OCIRepository, version),
		TofuStateBaseURL:  m.StateBaseURL(),
		DashboardHostname: c.DashboardHostname(),
		ArtifactoryURL:    m.FilesBase,
		VMModuleURL:       fmt.Sprintf("%s/tofu-modules/vm/%s/vm.tar.gz", m.FilesRepoURL(), version),
		AnsibleContentURL: fmt.Sprintf("%s/ansible/rondo/%s/rondo-ansible.tar.gz", m.FilesRepoURL(), version),
	}
}
