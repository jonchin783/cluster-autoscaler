/*
Copyright 2019 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package cisco

import (
	"fmt"
	"io"

	"k8s.io/autoscaler/cluster-autoscaler/cloudprovider"
	"k8s.io/autoscaler/cluster-autoscaler/config"
	schedulerframework "k8s.io/kubernetes/pkg/scheduler/framework/v1alpha1"
)

const (
	defaultManager = "rest"
)

// NodeRef stores the name, machineID and providerID of a node.
type NodeRef struct {
	Name       string
	MachineID  string
	ProviderID string
	IPs        []string
}

// ciscoManager is an interface for the basic interactions with the cluster.
type ciscoManager interface {
	nodeGroupSize(nodegroup string) (int, error)
	createNodes(nodegroup string, nodes int) error
	getNodes(nodegroup string) ([]string, error)
	getNodeNames(nodegroup string) ([]string, error)
	deleteNodes(nodegroup string, nodes []NodeRef, updatedNodeCount int) error
	templateNodeInfo(nodegroup string) (*schedulerframework.NodeInfo, error)
}

// createCiscoManager creates the desired implementation of ciscoManager.
// Currently reads the environment variable PACKET_MANAGER to find which to create,
// and falls back to a default if the variable is not found.
func createCiscoManager(configReader io.Reader, discoverOpts cloudprovider.NodeGroupDiscoveryOptions, opts config.AutoscalingOptions) (ciscoManager, error) {
	// Cisco Container Platform uses rest api as default Manager, currently no go sdk available
	manager := defaultManager

	switch manager {
	case "rest":
		return createCiscoManagerRest(configReader, discoverOpts, opts)
	}

	return nil, fmt.Errorf("Cisco Container Platform manager does not exist: %s", manager)
}
