package bootstrap

import (
	"fmt"

	"rondo/internal/config"
	"rondo/internal/kobj"
	"rondo/internal/tofustate"
)

func PlatformInstallFor(c *config.Config, version string) kobj.PlatformInstall {
	m := c.Mirror()
	p := kobj.PlatformInstall{
		ControllerImage:   fmt.Sprintf("%s/rondo-controller:%s", m.OCIRepository, version),
		ToolboxImage:      fmt.Sprintf("%s/rondo-toolbox:%s", m.OCIRepository, version),
		TofuStateBaseURL:  m.StateBaseURL(),
		DashboardHostname: c.DashboardHostname(),
		MirrorFilesURL:    m.FilesBase,
		VMModuleURL:       fmt.Sprintf("%s/tofu-modules/vm/%s/vm.tar.gz", m.FilesRepoURL(), version),
		AnsibleContentURL: fmt.Sprintf("%s/ansible/rondo/%s/rondo-ansible.tar.gz", m.FilesRepoURL(), version),
	}
	if m.StateBackend == tofustate.S3 {
		p.TofuStateS3 = m.StateS3.Encode()
	}
	return p
}
