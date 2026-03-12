package deployer

import (
	"fmt"

	"github.com/vibed-project/vibeD/pkg/api"
)

// Factory provides the right Deployer based on the selected deployment target.
type Factory struct {
	deployers map[api.DeploymentTarget]Deployer
}

// NewFactory creates a Factory with the registered deployers.
func NewFactory() *Factory {
	return &Factory{
		deployers: make(map[api.DeploymentTarget]Deployer),
	}
}

// Register adds a deployer for a specific target.
func (f *Factory) Register(target api.DeploymentTarget, d Deployer) {
	f.deployers[target] = d
}

// Get returns the deployer for the given target.
func (f *Factory) Get(target api.DeploymentTarget) (Deployer, error) {
	d, ok := f.deployers[target]
	if !ok {
		return nil, fmt.Errorf("no deployer registered for target %q", target)
	}
	return d, nil
}
