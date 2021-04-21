# Cluster Autoscaler for Cisco Container Platform

The cluster autoscaler for Cisco Container Platform (CCP) tenant cluster worker nodes performs
autoscaling within any specified nodepool. It will run as a `Deployment` in your cluster. 
It is best recommended to run the autoscaling deployment on the master node(s)

This README will go over some of the necessary steps required to get
the cluster autoscaler up and running.

## Permissions and credentials

The autoscaler needs a `ServiceAccount` with permissions for Kubernetes and requires the address
or domain name of the CCP control plane, for example, https://asean-hx-ccp3.cisco.com, and the
username and password credentials for invoking REST APIs to the CCP control plane for scaling

An example `ServiceAccount` is given in [examples/cluster-autoscaler-svcaccount.yaml](examples/cluster-autoscaler-svcaccount.yaml).

## Deploying the CCP tenant cluster with a 1:1 mapping between nodegroup and worker node

Due to a current limitation in the CCP control plane, we can scale down the size of a nodegroup
but we cannot delete a specific worker node from the nodegroup. Hence, this may result in deletion 
of an unintended worker node in the nodegroup during scale down, and cause disruption when the 
evicted pods are re-spawned. 

To overcome this, we need to deploy the CCP tenant cluster with a one-to-one mapping between 
nodegroup and worker node. The autoscaler code will treat each nodegroup deployed in the CCP
tenant cluster as a single worker node. Collectively, the nodegroups in the CCP tenant cluster
form the worker nodes in a single nodegroup in autoscaler. 

To ensure the uniqueness of each nodegroup (worker node) in CCP and autoscaler, the following
naming convention is adopted - nodegroup-"random 6 digit number"

For example, a CCP tenant cluster with 2 worker nodes can have the following structure:

- nodegroup-111111 (with one worker node)
- nodegroup-222222 (with one worker node)
 
### Label the master node to deploy autoscaler pod

For example, use the following step to label your master node

```yaml
kubectl nodes label admin-250771-master-gro-ec7939fbbf dedicated=master
```

### For example, apply the following manifest on your CCP tenant cluster to create the service acounts
```yaml
---
  apiVersion: v1
  kind: ServiceAccount
  metadata:
    labels:
      k8s-addon: cluster-autoscaler.addons.k8s.io
      k8s-app: cluster-autoscaler
    name: cluster-autoscaler
    namespace: kube-system
  ---
  apiVersion: rbac.authorization.k8s.io/v1
  kind: ClusterRole
  metadata:
    name: cluster-autoscaler
    labels:
      k8s-addon: cluster-autoscaler.addons.k8s.io
      k8s-app: cluster-autoscaler
  rules:
    - apiGroups: [""]
      resources: ["events", "endpoints"]
      verbs: ["create", "patch"]
    - apiGroups: [""]
      resources: ["pods/eviction"]
      verbs: ["create"]
    - apiGroups: [""]
      resources: ["pods/status"]
      verbs: ["update"]
    - apiGroups: [""]
      resources: ["endpoints"]
      resourceNames: ["cluster-autoscaler"]
      verbs: ["get", "update"]
    - apiGroups: [""]
      resources: ["nodes"]
      verbs: ["watch", "list", "get", "update"]
    - apiGroups: [""]
      resources:
        - "pods"
        - "services"
        - "replicationcontrollers"
        - "persistentvolumeclaims"
        - "persistentvolumes"
      verbs: ["watch", "list", "get"]
    - apiGroups: ["extensions"]
      resources: ["replicasets", "daemonsets"]
      verbs: ["watch", "list", "get"]
    - apiGroups: ["policy"]
      resources: ["poddisruptionbudgets"]
      verbs: ["watch", "list"]
    - apiGroups: ["apps"]
      resources: ["statefulsets", "replicasets", "daemonsets"]
      verbs: ["watch", "list", "get"]
    - apiGroups: ["storage.k8s.io"]
      resources: ["storageclasses", "csinodes"]
      verbs: ["watch", "list", "get"]
    - apiGroups: ["batch", "extensions"]
      resources: ["jobs"]
      verbs: ["get", "list", "watch", "patch"]
    - apiGroups: ["coordination.k8s.io"]
      resources: ["leases"]
      verbs: ["create"]
    - apiGroups: ["coordination.k8s.io"]
      resourceNames: ["cluster-autoscaler"]
      resources: ["leases"]
      verbs: ["get", "update"]
    - apiGroups: [""]
      resources: ["configmaps"]
      verbs: ["create","list","watch"]
    - apiGroups: [""]
      resources: ["configmaps"]
      resourceNames: ["cluster-autoscaler-status", "cluster-autoscaler-priority-expander"]
      verbs: ["delete", "get", "update", "watch"]
  ---
  apiVersion: rbac.authorization.k8s.io/v1
  kind: Role
  metadata:
    name: cluster-autoscaler
    namespace: kube-system
    labels:
      k8s-addon: cluster-autoscaler.addons.k8s.io
      k8s-app: cluster-autoscaler
  rules:
    - apiGroups: [""]
      resources: ["configmaps"]
      verbs: ["create","list","watch"]
    - apiGroups: [""]
      resources: ["configmaps"]
      resourceNames: ["cluster-autoscaler-status", "cluster-autoscaler-priority-expander"]
      verbs: ["delete", "get", "update", "watch"]
  
  ---
  apiVersion: rbac.authorization.k8s.io/v1
  kind: ClusterRoleBinding
  metadata:
    name: cluster-autoscaler
    namespace: kube-system
    labels:
      k8s-addon: cluster-autoscaler.addons.k8s.io
      k8s-app: cluster-autoscaler
  roleRef:
    apiGroup: rbac.authorization.k8s.io
    kind: ClusterRole
    name: cluster-autoscaler
  subjects:
    - kind: ServiceAccount
      name: cluster-autoscaler
      namespace: kube-system
  
  ---
  apiVersion: rbac.authorization.k8s.io/v1
  kind: RoleBinding
  metadata:
    name: cluster-autoscaler
    namespace: kube-system
    labels:
      k8s-addon: cluster-autoscaler.addons.k8s.io
      k8s-app: cluster-autoscaler
  roleRef:
    apiGroup: rbac.authorization.k8s.io
    kind: Role
    name: cluster-autoscaler
  subjects:
    - kind: ServiceAccount
      name: cluster-autoscaler
      namespace: kube-system
```

## Autoscaler deployment

The deployment in `examples/cluster-autoscaler-deployment.yaml` can be used, but the arguments passed to the autoscaler will need to be changed to match your cluster.

- --cloud-provider - cisco
- --ccp-address    - address or domain name of your CCP control plane
- --ccp-username   - administrator username allowed to invoke REST APIs
- --ccp-password   - administrator password 
- --cluster-name   - name of your CCP tenant cluster
- --nodes          - of the form `min:max:NodepoolName`. Only a single node pool is currently supported.                        |

### For example, apply the following deployment manifest with kubectl onto your CCP tenant cluster

```yaml
---
  apiVersion: v1
  data:
    .dockerconfigjson: eyJhdXRocyI6eyIxMC42OC4yOS40MTo1MDAwIjp7InVzZXJuYW1lIjoiYWRtaW4iLCJwYXNzd29yZCI6ImNpc2NvMTIzIiwiZW1haWwiOiJqb25hY2hpbkBjaXNjby5jb20iLCJhdXRoIjoiWVdSdGFXNDZZMmx6WTI4eE1qTT0ifX19
  kind: Secret
  metadata:
    name: my-secret
    namespace: kube-system
  type: kubernetes.io/dockerconfigjson  
---
  apiVersion: extensions/v1beta1
  kind: Deployment
  metadata:
    name: cluster-autoscaler
    namespace: kube-system
    labels:
      app: cluster-autoscaler
  spec:
    progressDeadlineSeconds: 600
    replicas: 1
    revisionHistoryLimit: 10
    selector:
      matchLabels:
        app: cluster-autoscaler
    strategy:
      rollingUpdate:
        maxSurge: 25%
        maxUnavailable: 25%
      type: RollingUpdate
    template:
      metadata:
        labels:
          app: cluster-autoscaler
      spec:
        containers:
        - command:
          - ./cluster-autoscaler
          - --v=4
          - --stderrthreshold=info
          - --cloud-provider=cisco
          - --cluster-name=<cluster-name>
          - --ccp-address=<ccp address>
          - --ccp-username=<ccp username>
          - --ccp-password=<ccp password>
          - --skip-nodes-with-local-storage=false
          - --expander=least-waste
          - --nodes=1:5:<pool name>
          image: 10.68.29.41:5000/autoscaling-builder
          imagePullPolicy: Always
          name: cluster-autoscaler
          resources:
            limits:
              cpu: 100m
              memory: 300Mi
            requests:
              cpu: 100m
              memory: 300Mi
          terminationMessagePath: /dev/termination-log
          terminationMessagePolicy: File
        dnsPolicy: ClusterFirst
        imagePullSecrets:
        - name: my-secret
        restartPolicy: Always
        schedulerName: default-scheduler
        securityContext: {}
        serviceAccount: cluster-autoscaler
        serviceAccountName: cluster-autoscaler
        terminationGracePeriodSeconds: 30
```

## Notes

The autoscaler will not remove nodes which have non-default kube-system pods.
This prevents the node that the autoscaler is running on from being scaled down.
If you are deploying the autoscaler into a cluster which already has more than one node,
it is best to deploy it onto the master node or on any node which already has non-default
kube-system pods, to minimise the number of nodes which cannot be removed when scaling.
