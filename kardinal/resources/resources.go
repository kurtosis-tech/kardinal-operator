package resources

import (
	"context"

	"github.com/kurtosis-tech/stacktrace"
	"github.com/sirupsen/logrus"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"kardinal.dev/kardinal-operator/kardinal/topology"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func Reconcile(ctx context.Context, cl client.Client) error {
	logrus.Info("Flow reconcile")
	// Get k8s resources
	namespaceStr := "baseline"
	namespace, err := getNamespaceResources(ctx, namespaceStr, cl)
	if err != nil {
		return stacktrace.Propagate(err, "An error occurred retrieving the list of resources for namespace %s", namespace)
	}
	// Generate base cluster topology
	_, err = topology.NewClusterTopologyFromResources(namespace.Services, namespace.Deployments, namespaceStr, namespaceStr)
	if err != nil {
		return stacktrace.Propagate(err, "An error occurred generating the base cluster topology")
	}
	// Generate flow topologies
	// Merge topologies
	// Generate k8s resources
	// Reconcile
	return nil
}

func getNamespaceResources(ctx context.Context, namespace string, cl client.Client) (*Namespace, error) {

	services := &corev1.ServiceList{}
	err := cl.List(ctx, services, client.InNamespace(namespace))
	if err != nil {
		return nil, stacktrace.Propagate(err, "An error occurred retrieving the list of services for namespace %s", namespace)
	}

	deployments := &appsv1.DeploymentList{}
	err = cl.List(ctx, deployments, client.InNamespace(namespace))
	if err != nil {
		return nil, stacktrace.Propagate(err, "An error occurred retrieving the list of deployments for namespace %s", namespace)
	}

	return &Namespace{
		Services:    services,
		Deployments: deployments,
	}, nil
}
