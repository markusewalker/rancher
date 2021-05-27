package cluster

import (
	"fmt"
	"time"

	v3 "github.com/rancher/rancher/pkg/apis/management.cattle.io/v3"
	provisioningv1api "github.com/rancher/rancher/pkg/apis/provisioning.cattle.io/v1"
	"github.com/rancher/rancher/tests/integration/pkg/clients"
	"github.com/rancher/rancher/tests/integration/pkg/defaults"
	"github.com/rancher/rancher/tests/integration/pkg/namespace"
	"github.com/rancher/rancher/tests/integration/pkg/nodeconfig"
	"github.com/rancher/rancher/tests/integration/pkg/registry"
	"github.com/rancher/rancher/tests/integration/pkg/wait"
	"github.com/rancher/wrangler/pkg/condition"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
	capi "sigs.k8s.io/cluster-api/api/v1alpha4"
)

func New(clients *clients.Clients, cluster *provisioningv1api.Cluster) (*provisioningv1api.Cluster, error) {
	cluster = cluster.DeepCopy()
	if cluster.Namespace == "" {
		newNs, err := namespace.Random(clients)
		if err != nil {
			return nil, err
		}
		cluster.Namespace = newNs.Name
	}

	if cluster.Name == "" && cluster.GenerateName == "" {
		cluster.GenerateName = "test-cluster-"
	}

	if cluster.Spec.KubernetesVersion == "" {
		cluster.Spec.KubernetesVersion = defaults.SomeK8sVersion
	}

	if cluster.Spec.RKEConfig != nil {
		if cluster.Spec.RKEConfig.ControlPlaneConfig.Data == nil {
			cluster.Spec.RKEConfig.ControlPlaneConfig.Data = map[string]interface{}{}
		}
		for k, v := range defaults.CommonClusterConfig {
			cluster.Spec.RKEConfig.ControlPlaneConfig.Data[k] = v
		}

		for i, np := range cluster.Spec.RKEConfig.MachinePools {
			if np.NodeConfig == nil {
				podConfig, err := nodeconfig.NewPodConfig(clients, cluster.Namespace)
				if err != nil {
					return nil, err
				}
				cluster.Spec.RKEConfig.MachinePools[i].NodeConfig = podConfig
			}
			if np.Name == "" {
				cluster.Spec.RKEConfig.MachinePools[i].Name = fmt.Sprintf("pool-%d", i)
			}
		}

		if cluster.Spec.RKEConfig.Registries == nil {
			registryConfig, err := registry.GetCache(clients, cluster.Namespace)
			if err != nil {
				return nil, err
			}
			cluster.Spec.RKEConfig.Registries = &registryConfig
		}
	}

	return clients.Provisioning.Cluster().Create(cluster)
}

func Machines(clients *clients.Clients, cluster *provisioningv1api.Cluster) (*capi.MachineList, error) {
	return clients.CAPI.Machine().List(cluster.Namespace, metav1.ListOptions{
		LabelSelector: "cluster.x-k8s.io/cluster-name=" + cluster.Name,
	})
}

func WaitFor(clients *clients.Clients, c *provisioningv1api.Cluster) (*provisioningv1api.Cluster, error) {
	err := wait.Object(clients.Ctx, clients.Provisioning.Cluster().Watch, c, func(obj runtime.Object) (bool, error) {
		c = obj.(*provisioningv1api.Cluster)
		return c.Status.ClusterName != "", nil
	})
	if err != nil {
		return nil, err
	}

	mgmtCluster, err := clients.Mgmt.Cluster().Get(c.Status.ClusterName, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}

	err = wait.Object(clients.Ctx, func(namespace string, opts metav1.ListOptions) (watch.Interface, error) {
		return clients.Mgmt.Cluster().Watch(opts)
	}, mgmtCluster, func(obj runtime.Object) (bool, error) {
		mgmtCluster = obj.(*v3.Cluster)
		return condition.Cond("Ready").IsTrue(mgmtCluster), nil
	})
	if err != nil {
		return nil, err
	}

	err = wait.Object(clients.Ctx, clients.Provisioning.Cluster().Watch, c, func(obj runtime.Object) (bool, error) {
		c = obj.(*provisioningv1api.Cluster)
		return c.Status.Ready, nil
	})
	if err != nil {
		return nil, err
	}

	machines, err := Machines(clients, c)
	if err != nil {
		return nil, err
	}

	for _, machine := range machines.Items {
		err = wait.Object(clients.Ctx, clients.CAPI.Machine().Watch, &machine, func(obj runtime.Object) (bool, error) {
			machine := obj.(*capi.Machine)
			return machine.Status.NodeRef != nil, nil
		})
		if err != nil {
			return nil, err
		}
	}

	return c, nil
}

func CustomCommand(clients *clients.Clients, c *provisioningv1api.Cluster) (string, error) {
	err := wait.Object(clients.Ctx, clients.Provisioning.Cluster().Watch, c, func(obj runtime.Object) (bool, error) {
		c = obj.(*provisioningv1api.Cluster)
		return c.Status.ClusterName != "", nil
	})
	if err != nil {
		return "", err
	}

	for i := 0; i < 15; i++ {
		tokens, err := clients.Mgmt.ClusterRegistrationToken().List(c.Status.ClusterName, metav1.ListOptions{})
		if err != nil || len(tokens.Items) == 0 || tokens.Items[0].Status.NodeCommand == "" {
			time.Sleep(time.Second)
			continue
		}
		return tokens.Items[0].Status.InsecureNodeCommand, nil
	}

	return "", fmt.Errorf("timeout getting custom command")
}
