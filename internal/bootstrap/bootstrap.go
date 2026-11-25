package bootstrap

import (
	"rondo/internal/config"
	"rondo/internal/constructs"
	"rondo/internal/synth"
)

func All(c *config.Config) []synth.Component {
	return []synth.Component{
		&constructs.PKIStack{
			CommonName:      c.ClusterCACommonName(),
			RancherHostname: c.RancherHostname(),
		},
		&constructs.MetalLBConfig{
			IngressIP: c.Targets.Management.IngressLoadBalancerIP,
		},
	}
}
