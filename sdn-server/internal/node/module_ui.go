package node

import (
	"fmt"
	"github.com/spacedatanetwork/sdn-server/internal/modulert"
)

func (n *Node) ModuleApplication(id string) ([]byte, error) {
	module, ok := n.plugins.Get(id).(*modulert.Module)
	if !ok {
		return nil, fmt.Errorf("module %q is not installed", id)
	}
	return module.ApplicationRecord()
}

func (n *Node) ModuleApplicationArtifact(id string) ([]byte, error) {
	module, ok := n.plugins.Get(id).(*modulert.Module)
	if !ok {
		return nil, fmt.Errorf("module %q is not installed", id)
	}
	return module.ApplicationArtifact()
}

func (n *Node) ModuleApplicationPage(id string) ([]byte, error) {
	module, ok := n.plugins.Get(id).(*modulert.Module)
	if !ok {
		return nil, fmt.Errorf("module %q is not installed", id)
	}
	return module.ApplicationPage()
}
