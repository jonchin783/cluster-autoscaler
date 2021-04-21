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
	"bytes"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"math/big"
	mrand "math/rand"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"text/template"
	"time"

	"gopkg.in/gcfg.v1"
	"k8s.io/autoscaler/cluster-autoscaler/cloudprovider"
	"k8s.io/autoscaler/cluster-autoscaler/config"
	"k8s.io/klog"
	schedulerframework "k8s.io/kubernetes/pkg/scheduler/framework/v1alpha1"
)

type ciscoManagerRest struct {
	baseURL           string
	clusterName       string
	projectID         string
	apiServerEndpoint string
	facility          string
	plan              string
	os                string
	billing           string
	cloudinit         string
	reservation       string
	hostnamePattern   string
	ccpUsername       string
	ccpPassword       string
	waitTimeStep      time.Duration
}

// ConfigGlobal options only include the project-id for now
type ConfigGlobal struct {
	ClusterName       string `gcfg:"cluster-name"`
	ProjectID         string `gcfg:"project-id"`
	APIServerEndpoint string `gcfg:"api-server-endpoint"`
	Facility          string `gcfg:"facility"`
	Plan              string `gcfg:"plan"`
	OS                string `gcfg:"os"`
	Billing           string `gcfg:"billing"`
	CloudInit         string `gcfg:"cloudinit"`
	Reservation       string `gcfg:"reservation"`
	HostnamePattern   string `gcfg:"hostname-pattern"`
}

// ConfigFile is used to read and store information from the cloud configuration file
type ConfigFile struct {
	Global ConfigGlobal `gcfg:"global"`
}

// Device represents a device
type Device struct {
	ID          string   `json:"id"`
	ShortID     string   `json:"short_id"`
	Hostname    string   `json:"hostname"`
	Description string   `json:"description"`
	State       string   `json:"state"`
	Tags        []string `json:"tags"`
}

// Devices represents a list of devices
type Devices struct {
	Devices []Device `json:"devices"`
}

// IPAddressCreateRequest represents a request to create a new IP address within a DeviceCreateRequest
type IPAddressCreateRequest struct {
	AddressFamily int  `json:"address_family"`
	Public        bool `json:"public"`
}

// DeviceCreateRequest represents a request to create a new device. Used by createNodes
type DeviceCreateRequest struct {
	Hostname              string                   `json:"hostname"`
	Plan                  string                   `json:"plan"`
	Facility              []string                 `json:"facility"`
	OS                    string                   `json:"operating_system"`
	BillingCycle          string                   `json:"billing_cycle"`
	ProjectID             string                   `json:"project_id"`
	UserData              string                   `json:"userdata"`
	Storage               string                   `json:"storage,omitempty"`
	Tags                  []string                 `json:"tags"`
	CustomData            string                   `json:"customdata,omitempty"`
	IPAddresses           []IPAddressCreateRequest `json:"ip_addresses,omitempty"`
	HardwareReservationID string                   `json:"hardware_reservation_id,omitempty"`
}

// CloudInitTemplateData represents the variables that can be used in cloudinit templates
type CloudInitTemplateData struct {
	BootstrapTokenID     string
	BootstrapTokenSecret string
	APIServerEndpoint    string
}

// HostnameTemplateData represents the template variables used to construct host names for new nodes
type HostnameTemplateData struct {
	ClusterName string
	NodeGroup   string
	RandString8 string
}

// AuthenticateCcpRequest represents ccp Username and Password - Jonathan Chin, 6/5/20
type AuthenticateCcpRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// Cisco Container Platform important getCluster fields - Jonathan Chin, 6/5/20
type clusterInfo struct {
	ID         string      `json:"id"`
	Type       string      `json:"type"`
	Name       string      `json:"name"`
	Provider   string      `json:"provider"`
	Nodegroups []nodeGroup `json:"node_groups"`
}

// Cisco Container Platform nodeGroup structure - Jonathan Chin, 6/5/20
type nodeGroup struct {
	Name     string `json:"name"`
	Size     int    `json:"size"`
	Template string `json:"template"`
	Vcpu     int    `json:"vcpus"`
}

// Information for the candidate cluster selected for autoscaling - Jonathan Chin, 6/5/20
type candidateCluster struct {
	ID            string
	NodegroupName string
	Size          int
}

// Inner payload for Cluster Scaling - Jonathan Chin, 6/5/20
type ccpNodegroup struct {
	Group string `json:"name"`
	Size  int    `json:"size"`
}

// Outer payload for Cluster Scaling - Jonathan Chin, 6/5/20
type ccpScalePayload struct {
	Groups []ccpNodegroup `json:"node_groups"`
}

type nodeName struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Phase  string `json:"phase"`
}

type clusterNodeGroup struct {
	Name     string     `json:"name"`
	Size     int        `json:"size"`
	Template string     `json:"template"`
	Vcpu     int        `json:"vcpus"`
	Nodes    []nodeName `json:"nodes"`
}

type clusterNodes struct {
	ID         string             `json:"id"`
	Type       string             `json:"type"`
	Name       string             `json:"name"`
	Provider   string             `json:"provider"`
	Nodegroups []clusterNodeGroup `json:"node_groups"`
}

// CCP add new nodegroup payload
type ccpNewNodeName struct {
	NodeName string `json:"name"`
	Status   string `json:"status"`
	Phase    string `json:"phase"`
}

// CCP add new nodegroup payload
type ccpNewNodegroup struct {
	Name          string           `json:"name"`
	Size          int              `json:"size"`
	Template      string           `json:"template"`
	CPU           int              `json:"vcpus"`
	Memory        int              `json:"memory_mb"`
	SSHuser       string           `json:"ssh_user"`
	SSHkey        string           `json:"ssh_key"`
	Nodes         []ccpNewNodeName `json:"nodes"`
	KubernetesVer string           `json:"kubernetes_version"`
}

// CCP get cluster uuid node-group
type ccpNewNodegroups struct {
	Results []ccpNewNodegroup `json:"results"`
}

// Devices represents a list of ccp worker nodes
type workerNodes struct {
	Worker []string
}

// Find returns the smallest index i at which x == a[i],
// or len(a) if there is no such index.
func Find(a []string, x string) int {
	for i, n := range a {
		if x == n {
			return i
		}
	}
	return len(a)
}

// Contains tells whether a contains x.
func Contains(a []string, x string) bool {
	for _, n := range a {
		if x == n {
			return true
		}
	}
	return false
}

// createCiscoManagerRest sets up the client and returns
// an ciscoManagerRest.
func createCiscoManagerRest(configReader io.Reader, discoverOpts cloudprovider.NodeGroupDiscoveryOptions, opts config.AutoscalingOptions) (*ciscoManagerRest, error) {
	var cfg ConfigFile
	if configReader != nil {
		if err := gcfg.ReadInto(&cfg, configReader); err != nil {
			klog.Errorf("Couldn't read config: %v", err)
			return nil, err
		}
	}

	if opts.ClusterName == "" && cfg.Global.ClusterName == "" {
		klog.Fatalf("The cluster-name parameter must be set")
	} else if opts.ClusterName != "" && cfg.Global.ClusterName == "" {
		cfg.Global.ClusterName = opts.ClusterName
	}
	klog.Infof("Cluster name is: %s", cfg.Global.ClusterName)
	klog.Infof("CCP Address is: %s", opts.CiscoCcpAddress)
	klog.Infof("CCP Username is: %s", opts.CiscoCcpUsername)
	klog.Infof("CCP Password is: %s", opts.CiscoCcpPassword)

	manager := ciscoManagerRest{
		baseURL:           opts.CiscoCcpAddress,
		clusterName:       cfg.Global.ClusterName,
		projectID:         cfg.Global.ProjectID,
		apiServerEndpoint: cfg.Global.APIServerEndpoint,
		facility:          cfg.Global.Facility,
		plan:              cfg.Global.Plan,
		os:                cfg.Global.OS,
		billing:           cfg.Global.Billing,
		cloudinit:         cfg.Global.CloudInit,
		reservation:       cfg.Global.Reservation,
		hostnamePattern:   cfg.Global.HostnamePattern,
		ccpUsername:       opts.CiscoCcpUsername,
		ccpPassword:       opts.CiscoCcpPassword,
	}
	return &manager, nil
}

func (mgr *ciscoManagerRest) listCiscoDevices() ([]string, error) {
	klog.Infof("Inside listCiscoDevices")
	var workers []string
	// First, perform token authentication with CCP control plane
	ccp := AuthenticateCcpRequest{
		Username: mgr.ccpUsername,
		Password: mgr.ccpPassword,
	}
	authURL := mgr.baseURL + "/v3/system/login"
	_, token, err := authenticateToken(&ccp, authURL)
	if err != nil {
		klog.Errorf("Failed to authenticate with Cisco Container Platform control plane: %v", err)
		panic(err)
	} else {
		klog.Infof("Authentication Token is: %s", token)
	}
	getNodesURL := mgr.baseURL + "/v3/clusters/"
	klog.Infof("Before entry to buildClusterNodes")
	resp, workers, _ := buildClusterNodes(token, mgr.clusterName, getNodesURL)
	if err != nil {
		klog.Errorf("Failed to fetch CCP tenant cluster: %v", err)
		panic(err)
	} else {
		if resp.StatusCode == 200 {
			klog.Infof("Updated latest set of worker nodes: %s", workers)
			return workers, nil
		}
	}
	return workers, fmt.Errorf(resp.Status, resp.Body)
}

// nodeGroupSize gets the current size of the nodegroup as reported by ccp.
func (mgr *ciscoManagerRest) nodeGroupSize(nodegroup string) (int, error) {
	klog.Infof("Entry to nodeGroupSize")
	s, _ := mgr.listCiscoDevices()
	// Get the count of devices tagged as nodegroup members
	klog.Infof("s node group list is %s", s)
	count := 0
	for _, d := range s {
		//if Contains(d.Tags, "k8s-cluster-"+mgr.clusterName) && Contains(d.Tags, "k8s-nodepool-"+nodegroup) {
		//	count++
		//}
		klog.Infof("d value is %s", d)
		klog.Infof("substring is %s", mgr.clusterName+"-nodegroup")
		if strings.Contains(d, mgr.clusterName+"-nodegroup") {
			count++
		}
	}
	klog.V(3).Infof("Nodegroup %s: %d/%d", nodegroup, count, len(s))
	return count, nil
}

func randString8() string {
	n := 8
	mrand.Seed(time.Now().UnixNano())
	letterRunes := []rune("acdefghijklmnopqrstuvwxyz")
	b := make([]rune, n)
	for i := range b {
		//b[i] = letterRunes[rand.Int(len(letterRunes))]
		b[i] = letterRunes[mrand.Intn(len(letterRunes))]
	}
	return string(b)
}

func (mgr *ciscoManagerRest) createNode(cloudinit, nodegroup string) {

	ccp := AuthenticateCcpRequest{
		Username: mgr.ccpUsername,
		Password: mgr.ccpPassword,
	}
	authURL := mgr.baseURL + "/v3/system/login"
	_, token, err := authenticateToken(&ccp, authURL)
	if err != nil {
		klog.Errorf("Failed to authenticate with Cisco Container Platform control plane: %v", err)
		panic(err)
	} else {
		klog.Infof("Token is %s", token)
	}

	getClusterURL := mgr.baseURL + "/v3/clusters/"
	_, uuid, _, _, err := fetchCandiateCluster(token, mgr.clusterName, getClusterURL)
	if err != nil {
		klog.Errorf("Failed to fetch CCP tenant cluster: %v", err)
		panic(err)
	} else {
		//var newsize = size + 1
		//var x = ccpNodegroup{node, newsize}
		getTemplateURL := mgr.baseURL + "/v3/clusters/" + uuid + "/node-groups/"
		templateGrp, err := getNodegroupTemplate(token, getTemplateURL)
		s := genNodegroupNum()
		str := strconv.FormatInt(s, 10)
		cr := ccpNewNodegroup{
			Name:          "nodegroup-" + str,
			Size:          1,
			Template:      templateGrp.Results[0].Template,
			CPU:           templateGrp.Results[0].CPU,
			Memory:        templateGrp.Results[0].Memory,
			SSHuser:       templateGrp.Results[0].SSHuser,
			SSHkey:        templateGrp.Results[0].SSHkey,
			KubernetesVer: templateGrp.Results[0].KubernetesVer,
		}

		createNodeURL := mgr.baseURL + "/v3/clusters/" + uuid + "/node-groups/"
		resp, err := createNewNodegroup(&cr, token, createNodeURL)
		//scaleURL := mgr.baseURL + "/v3/clusters/" + uuid + "/"
		//resp, err := scaleUpNode(&x, token, scaleURL)
		if resp.StatusCode == 201 {
			klog.Infof("Cisco Container Platform tenant cluster scaled up")
		} else {
			klog.Errorf("Failed to scale up Cisco Container Platform tenant cluster: %v", err)
			panic(err)
		}
	}
}

// createNodes provisions new nodes on cisco ccp and bootstraps them in the cluster.
func (mgr *ciscoManagerRest) createNodes(nodegroup string, nodes int) error {
	klog.Infof("Updating node count to %d for nodegroup %s", nodes, nodegroup)
	cloudinit, err := base64.StdEncoding.DecodeString(mgr.cloudinit)
	if err != nil {
		log.Fatal(err)
		return fmt.Errorf("Could not decode cloudinit script: %v", err)
	}

	for i := 0; i < nodes; i++ {
		mgr.createNode(string(cloudinit), nodegroup)
	}

	return nil
}

func authenticateCCP(cr *AuthenticateCcpRequest, baseURL string) (*http.Response, string, error) {
	url := baseURL + "/v3/system/login" + "?username=" + cr.Username + "&password=" + cr.Password
	jsonValue, _ := json.Marshal(cr)
	klog.Infof("Authenticating with Cisco Container Platform control plane cluster")
	klog.V(3).Infof("POST %s \n%v", url, string(jsonValue))
	req, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonValue))
	if err != nil {
		klog.Errorf("Failed to create request: %v", err)
		panic(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}
	client := &http.Client{Transport: tr}
	//client := &http.Client{}
	resp, err := client.Do(req)
	token := resp.Header.Get("X-AUTH-TOKEN")
	return resp, token, err
}

// getNodes should return ProviderIDs for all nodes in the node group,
// used to find any nodes which are unregistered in kubernetes.
func (mgr *ciscoManagerRest) getNodes(nodegroup string) ([]string, error) {
	// Get node ProviderIDs by getting device IDs from cisco ccp
	devices, err := mgr.listCiscoDevices()
	nodes := []string{}
	for _, d := range devices {
		if strings.Contains(d, mgr.clusterName+"-nodegroup") {
			nodes = append(nodes, d)
		}
	}
	return nodes, err
}

// getNodeNames should return Names for all nodes in the node group,
// used to find any nodes which are unregistered in kubernetes.
func (mgr *ciscoManagerRest) getNodeNames(nodegroup string) ([]string, error) {
	devices, err := mgr.listCiscoDevices()
	nodes := []string{}
	for _, d := range devices {
		klog.Infof("getNodeNames: %s", d)
		if strings.Contains(d, mgr.clusterName+"-nodegroup") {
			nodes = append(nodes, d)
		}
	}
	return nodes, err
}

// deleteNodes deletes nodes by passing a comma separated list of names or IPs
func (mgr *ciscoManagerRest) deleteNodes(nodegroup string, nodes []NodeRef, updatedNodeCount int) error {
	klog.Infof("Deleting nodes %v", nodes)
	for _, n := range nodes {
		klog.Infof("Inside deleting nodes")
		klog.Infof("Node %s - %s - %s", n.Name, n.MachineID, n.IPs)
		dl, _ := mgr.listCiscoDevices()
		klog.Infof("%d ccp tenant cluster nodes in total", len(dl))
		// Get the count of devices tagged as nodegroup
		for _, d := range dl {
			klog.Infof("Checking device %v", d)
			if strings.Contains(d, mgr.clusterName+"-nodegroup") {
				klog.Infof("nodegroup match %s %s", d, n.Name)
				if d == n.Name {
					klog.V(1).Infof("Matching cisco Device %s - %s", d, d)
					ccp := AuthenticateCcpRequest{
						Username: mgr.ccpUsername,
						Password: mgr.ccpPassword,
					}
					authURL := mgr.baseURL + "/v3/system/login"
					_, token, err := authenticateToken(&ccp, authURL)
					if err != nil {
						klog.Errorf("Failed to authenticate with Cisco Container Platform control plane: %v", err)
						panic(err)
					} else {
						klog.Infof("Authentication Token is: %s", token)
					}

					getClusterURL := mgr.baseURL + "/v3/clusters/"
					_, uuid, _, _, err := fetchCandiateCluster(token, mgr.clusterName, getClusterURL)
					if err != nil {
						klog.Errorf("Failed to retrieve the candidate CCP cluster info: %v", err)
						panic(err)
					} else {
						klog.Infof("Candidate CCP cluster uuid is: %s", uuid)
					}

					getTemplateURL := mgr.baseURL + "/v3/clusters/" + uuid + "/node-groups/"
					_, nodeName, err := fetchNodegroupName(token, d, getTemplateURL)
					if err != nil {
						klog.Errorf("Failed to retrieve the node group name containing worker node: %v", err)
						panic(err)
					} else {
						klog.Infof("CCP node group name is: %s", nodeName)
					}

					deleteURL := mgr.baseURL + "/v3/clusters/" + uuid + "/node-groups/" + nodeName + "/"
					resp, err := deleteNodegroup(token, deleteURL)
					if err != nil {
						klog.Fatalf("Error deleting node: %v", err)
						panic(err)
					}
					defer resp.Body.Close()
					body, _ := ioutil.ReadAll(resp.Body)
					klog.Infof("Deleted device %s: %v", d, body)
				}
			}
		}
	}
	return nil
}

// templateNodeInfo returns a NodeInfo with a node template based on the VM flavor
// that is used to created minions in a given node group.
func (mgr *ciscoManagerRest) templateNodeInfo(nodegroup string) (*schedulerframework.NodeInfo, error) {
	return nil, cloudprovider.ErrNotImplemented
}

func renderTemplate(str string, vars interface{}) string {
	tmpl, err := template.New("tmpl").Parse(str)

	if err != nil {
		panic(err)
	}
	var tmplBytes bytes.Buffer

	err = tmpl.Execute(&tmplBytes, vars)
	if err != nil {
		panic(err)
	}
	return tmplBytes.String()
}

func authenticateToken(cr *AuthenticateCcpRequest, baseURL string) (*http.Response, string, error) {
	data := url.Values{}
	data.Set("username", cr.Username)
	data.Set("password", cr.Password)
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}
	req, err := http.NewRequest("POST", baseURL, strings.NewReader(data.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	client := &http.Client{Transport: tr}
	resp, err := client.Do(req)
	token := resp.Header.Get("X-AUTH-TOKEN")
	return resp, token, err
}

func fetchCandiateCluster(token string, clusterName string, baseURL string) (*http.Response, string, string, int, error) {
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}
	req, err := http.NewRequest("GET", baseURL, nil)
	req.Header.Set("x-auth-token", token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Transport: tr}
	resp, err := client.Do(req)
	if resp.StatusCode == http.StatusOK {
		bodyBytes, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			log.Fatal(err)
		}
		//bodyString := string(bodyBytes)
		uuid, nodegroup, size, err := findClusterParams(clusterName, []byte(bodyBytes))
		if err != nil {
			log.Fatal(err)
		}
		return resp, uuid, nodegroup, size, err
	}
	return resp, "undefined", "undefined", 0, err
}

func findClusterParams(clusterName string, body []byte) (string, string, int, error) {
	var s []clusterInfo
	err := json.Unmarshal(body, &s)
	if err != nil {
		log.Fatal(err)
	}
	for _, a := range s {
		klog.Infof("Worker Node discovered: %s", a.Name)
		if a.Name == clusterName {
			return a.ID, a.Nodegroups[0].Name, a.Nodegroups[0].Size, err
		}
	}
	return "not_found", "not_found", 0, err
}

func scaleUpNode(cr *ccpNodegroup, token string, baseURL string) (*http.Response, error) {
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}

	jsondat := &ccpScalePayload{Groups: []ccpNodegroup{*cr}}
	encjson, _ := json.Marshal(jsondat)
	req, err := http.NewRequest("PATCH", baseURL, bytes.NewBuffer(encjson))
	req.Header.Set("x-auth-token", token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Transport: tr}
	resp, err := client.Do(req)
	return resp, err
}

func createNewNodegroup(cr *ccpNewNodegroup, token string, baseURL string) (*http.Response, error) {
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}
	jsonValue, _ := json.Marshal(cr)
	req, err := http.NewRequest("POST", baseURL, bytes.NewBuffer(jsonValue))
	req.Header.Set("x-auth-token", token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Transport: tr}
	resp, err := client.Do(req)
	return resp, err
}

func fetchNodegroupName(token string, targetNode string, baseURL string) (*http.Response, string, error) {
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}
	req, err := http.NewRequest("GET", baseURL, nil)
	req.Header.Set("x-auth-token", token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Transport: tr}
	resp, err := client.Do(req)
	if resp.StatusCode == http.StatusOK {
		bodyBytes, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			log.Fatal(err)
		}
		var nodegroupTemplate ccpNewNodegroups
		err2 := json.Unmarshal([]byte(bodyBytes), &nodegroupTemplate)
		if err2 != nil {
			log.Fatal(err2)
		}

		for _, a := range nodegroupTemplate.Results {
			klog.Infof("Template nodegroup: %v", a.Name)
			if a.Nodes[0].NodeName == targetNode {
				return resp, a.Name, err
			}
		}
	}
	return resp, "undefined", err
}

func buildClusterNodes(token string, clusterName string, baseURL string) (*http.Response, []string, error) {
	var s []string
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}
	req, err := http.NewRequest("GET", baseURL, nil)
	req.Header.Set("x-auth-token", token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Transport: tr}
	resp, err := client.Do(req)
	if resp.StatusCode == http.StatusOK {
		bodyBytes, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			log.Fatal(err)
		}
		//bodyString := string(bodyBytes)
		s, err := fetchClusterNodes(clusterName, []byte(bodyBytes))
		if err != nil {
			log.Fatal(err)
		}
		return resp, s, err
	}
	return resp, s, err
}

func fetchClusterNodes(clusterName string, body []byte) ([]string, error) {
	var s []clusterNodes
	var p []string
	err := json.Unmarshal(body, &s)
	if err != nil {
		log.Fatal(err)
	}
	for _, a := range s {
		if a.Name == clusterName {
			for _, b := range a.Nodegroups {
				for _, c := range b.Nodes {
					if c.Phase == "Running" || c.Phase == "CreationPending" {
						p = append(p, c.Name)
					}
				}
			}
		}
	}
	return p, err
}

func deleteNodegroup(token string, baseURL string) (*http.Response, error) {
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}
	req, err := http.NewRequest("DELETE", baseURL, bytes.NewBuffer([]byte("")))
	req.Header.Set("x-auth-token", token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Transport: tr}
	resp, err := client.Do(req)
	if err != nil {
		panic(err)
		// klog.Fatalf("Error listing nodes: %v", err)
	}
	defer resp.Body.Close()
	body, _ := ioutil.ReadAll(resp.Body)
	klog.Infof("Deleted worker node: %v", body)
	return resp, err
}

func getNodegroupTemplate(token string, baseURL string) (*ccpNewNodegroups, error) {
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}
	var jsonStr = []byte(``)
	req, err := http.NewRequest("GET", baseURL, bytes.NewBuffer(jsonStr))
	req.Header.Set("x-auth-token", token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Transport: tr}
	resp, err := client.Do(req)
	if err != nil {
		panic(err)
		// klog.Fatalf("Error listing nodes: %v", err)
	}
	defer resp.Body.Close()

	//klog.Infof("response Status: %s", resp.Status)
	var nodegroupTemplate ccpNewNodegroups
	if "200 OK" == resp.Status {
		body, _ := ioutil.ReadAll(resp.Body)
		json.Unmarshal([]byte(body), &nodegroupTemplate)
		return &nodegroupTemplate, nil
	}
	return &nodegroupTemplate, fmt.Errorf(resp.Status, resp.Body)
}

func genNodegroupNum() int64 {
	max := big.NewInt(999999)
	n, err := rand.Int(rand.Reader, max)
	if err != nil {
		log.Fatal(err)
	}
	return n.Int64()
}
