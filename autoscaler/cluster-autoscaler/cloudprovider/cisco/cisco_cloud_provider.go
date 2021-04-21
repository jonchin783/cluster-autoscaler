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
	"io"
	"os"
	"sync"

	apiv1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/autoscaler/cluster-autoscaler/cloudprovider"
	"k8s.io/autoscaler/cluster-autoscaler/config"
	"k8s.io/autoscaler/cluster-autoscaler/config/dynamic"
	"k8s.io/autoscaler/cluster-autoscaler/utils/errors"
	"k8s.io/klog"
)

const (
	// ProviderName is the cloud provider name for Cisco Container Platform
	ProviderName = "cisco"
	// GPULabel is the label added to nodes with GPU resource.
	GPULabel = "cloud.google.com/gke-accelerator"
)

var (
	availableGPUTypes = map[string]struct{}{
		"nvidia-tesla-v100": {},
	}
)

// ciscoCloudProvider implements CloudProvider interface from cluster-autoscaler/cloudprovider module.
type ciscoCloudProvider struct {
	ciscoManager    *ciscoManager
	resourceLimiter *cloudprovider.ResourceLimiter
	nodeGroups      []ciscoNodeGroup
}

// buildCiscoCloudProvider implements CloudProvider interface
func buildCiscoCloudProvider(ciscoManager ciscoManager, resourceLimiter *cloudprovider.ResourceLimiter) (cloudprovider.CloudProvider, error) {
	pcp := &ciscoCloudProvider{
		ciscoManager:    &ciscoManager,
		resourceLimiter: resourceLimiter,
		nodeGroups:      []ciscoNodeGroup{},
	}
	return pcp, nil
}

// Name returns the name of the cloud provider.
func (pcp *ciscoCloudProvider) Name() string {
	return ProviderName
}

// GPULabel returns the label added to nodes with GPU resource.
func (pcp *ciscoCloudProvider) GPULabel() string {
	return GPULabel
}

// GetAvailableGPUTypes return all available GPU types cloud provider supports
func (pcp *ciscoCloudProvider) GetAvailableGPUTypes() map[string]struct{} {
	return availableGPUTypes
}

// NodeGroups returns all node groups managed by this cloud provider.
func (pcp *ciscoCloudProvider) NodeGroups() []cloudprovider.NodeGroup {
	groups := make([]cloudprovider.NodeGroup, len(pcp.nodeGroups))
	for i, group := range pcp.nodeGroups {
		groups[i] = &group
	}
	return groups
}

// AddNodeGroup appends a node group to the list of node groups managed by this cloud provider.
func (pcp *ciscoCloudProvider) AddNodeGroup(group ciscoNodeGroup) {
	pcp.nodeGroups = append(pcp.nodeGroups, group)
}

// NodeGroupForNode returns the node group that a given node belongs to.
//
// Since only a single node group is currently supported, the first node group is always returned.
func (pcp *ciscoCloudProvider) NodeGroupForNode(node *apiv1.Node) (cloudprovider.NodeGroup, error) {
	if _, found := node.ObjectMeta.Labels["node-role.kubernetes.io/master"]; found {
		return nil, nil
	}
	return &(pcp.nodeGroups[0]), nil
}

// Pricing is not implemented.
func (pcp *ciscoCloudProvider) Pricing() (cloudprovider.PricingModel, errors.AutoscalerError) {
	return nil, cloudprovider.ErrNotImplemented
}

// GetAvailableMachineTypes is not implemented.
func (pcp *ciscoCloudProvider) GetAvailableMachineTypes() ([]string, error) {
	return []string{}, nil
}

// NewNodeGroup is not implemented.
func (pcp *ciscoCloudProvider) NewNodeGroup(machineType string, labels map[string]string, systemLabels map[string]string,
	taints []apiv1.Taint, extraResources map[string]resource.Quantity) (cloudprovider.NodeGroup, error) {
	return nil, cloudprovider.ErrNotImplemented
}

// GetResourceLimiter returns resource constraints for the cloud provider
func (pcp *ciscoCloudProvider) GetResourceLimiter() (*cloudprovider.ResourceLimiter, error) {
	return pcp.resourceLimiter, nil
}

// Refresh is called before every autoscaler main loop.
//
// Currently only prints debug information.
func (pcp *ciscoCloudProvider) Refresh() error {
	for _, nodegroup := range pcp.nodeGroups {
		klog.V(3).Info(nodegroup.Debug())
	}
	return nil
}

// Cleanup currently does nothing.
func (pcp *ciscoCloudProvider) Cleanup() error {
	return nil
}

// BuildCisco is called by the autoscaler to build a Cisco cloud provider.
//
// The ciscoManager is created here, and the node groups are created
// based on the specs provided via the command line parameters.
func BuildCisco(opts config.AutoscalingOptions, do cloudprovider.NodeGroupDiscoveryOptions, rl *cloudprovider.ResourceLimiter) cloudprovider.CloudProvider {
	var config io.ReadCloser

	if opts.CloudConfig != "" {
		var err error
		config, err = os.Open(opts.CloudConfig)
		if err != nil {
			klog.Fatalf("Couldn't open cloud provider configuration %s: %#v", opts.CloudConfig, err)
		}
		defer config.Close()
	}

	manager, err := createCiscoManager(config, do, opts)
	if err != nil {
		klog.Fatalf("Failed to create Cisco Container Platform manager: %v", err)
	}

	provider, err := buildCiscoCloudProvider(manager, rl)
	if err != nil {
		klog.Fatalf("Failed to create Cisco cloud provider: %v", err)
	}

	if len(do.NodeGroupSpecs) == 0 {
		klog.Fatalf("Must specify at least one node group with --nodes=<min>:<max>:<name>,...")
	}

	if len(do.NodeGroupSpecs) > 1 {
		klog.Fatalf("Cisco autoscaler only supports a single nodegroup for now")
	}

	clusterUpdateLock := sync.Mutex{}

	for _, nodegroupSpec := range do.NodeGroupSpecs {
		spec, err := dynamic.SpecFromString(nodegroupSpec, scaleToZeroSupported)
		if err != nil {
			klog.Fatalf("Could not parse node group spec %s: %v", nodegroupSpec, err)
		}

		ng := ciscoNodeGroup{
			ciscoManager:        manager,
			id:                  spec.Name,
			clusterUpdateMutex:  &clusterUpdateLock,
			minSize:             spec.MinSize,
			maxSize:             spec.MaxSize,
			targetSize:          new(int),
			waitTimeStep:        waitForStatusTimeStep,
			deleteBatchingDelay: deleteNodesBatchingDelay,
		}
		*ng.targetSize, err = ng.ciscoManager.nodeGroupSize(ng.id)
		if err != nil {
			klog.Fatalf("Could not set current nodes in node group: %v", err)
		}
		provider.(*ciscoCloudProvider).AddNodeGroup(ng)
	}

	return provider
}
