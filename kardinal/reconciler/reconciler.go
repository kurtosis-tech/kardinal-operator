package reconciler

import (
	"context"

	"github.com/brunoga/deep"
	"github.com/kurtosis-tech/stacktrace"
	"github.com/sirupsen/logrus"
	"kardinal.dev/kardinal-operator/kardinal/resources"
	"kardinal.dev/kardinal-operator/kardinal/topology"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func Reconcile(ctx context.Context, cl client.Client) error {
	logrus.Info("Reconciling")

	// Get k8s resources
	logrus.Infof("Get cluster resources")
	clusterResources, err := resources.NewResourcesFromClient(ctx, cl)
	if err != nil {
		return stacktrace.Propagate(err, "An error occurred retrieving the list of resources")
	}
	// Generate base cluster topology
	logrus.Info("Generate base cluster topology")
	baseClusterTopology, err := topology.NewClusterTopologyFromResources(clusterResources)
	if err != nil {
		return stacktrace.Propagate(err, "An error occurred generating the base cluster topology")
	}
	baseClusterTopology.Print()

	// Generate flow topologies
	flowTopologies := []*topology.ClusterTopology{}
	for _, namespace := range clusterResources.Namespaces {
		for _, flow := range namespace.Flows {
			logrus.Infof("Processing flow %s", flow.Name)

			flowTopology := baseClusterTopology.Copy(flow.Name)
			clusterGraph := flowTopology.GetGraph()

			service, err := baseClusterTopology.GetService(flow.Spec.Service, namespace.Name)
			if err != nil {
				return stacktrace.Propagate(err, "An error occurred retrieving base cluster topology service %s in namespace %s", flow.Spec.Service, namespace.Name)
			}
			deployment := resources.GetDeploymentFromName(service.ServiceID, namespace.Deployments)
			deploymentCopy := deep.MustCopy(deployment)
			deploymentCopy.Spec.Template.Spec.Containers[0].Image = flow.Spec.Image
			err = flowTopology.UpdateWithFlow(clusterGraph, flow.Name, service, &deploymentCopy.Spec)
			if err != nil {
				return stacktrace.Propagate(err, "An error occurred updating the base cluster topology with flow %s", flow.Name)
			}

			// Replace "baseline" version services with baseClusterTopology versions
			for idx, service := range flowTopology.Services {
				if !service.IsManaged {
					baseService, err := baseClusterTopology.GetService(service.ServiceID, service.Namespace)
					if err != nil {
						return stacktrace.Propagate(err, "An error occurred retrieving the baseline service %s", service.ServiceID)
					}
					flowTopology.Services[idx] = baseService
				}
			}

			// Update service dependencies
			for idx, dependency := range flowTopology.ServiceDependencies {
				if !dependency.Service.IsManaged {
					baseService, err := baseClusterTopology.GetService(dependency.Service.ServiceID, dependency.Service.Namespace)
					if err != nil {
						return stacktrace.Propagate(err, "An error occurred retrieving the baseline service %s for dependency %s", service.ServiceID, dependency.Service.ServiceID)
					}
					flowTopology.ServiceDependencies[idx].Service = baseService
				}
				if !dependency.DependsOnService.IsManaged {
					baseDependsOnService, err := baseClusterTopology.GetService(dependency.DependsOnService.ServiceID, dependency.DependsOnService.Namespace)
					if err != nil {
						return stacktrace.Propagate(err, "An error occurred retrieving the baseline service %s for depends on", dependency.DependsOnService.ServiceID)
					}
					flowTopology.ServiceDependencies[idx].DependsOnService = baseDependsOnService
				}
			}

			flowTopology.Print()
			flowTopologies = append(flowTopologies, flowTopology)
		}
	}

	// Merge flow topologies with base topology
	baseClusterTopology = baseClusterTopology.Merge(flowTopologies)
	baseClusterTopology.Print()

	// Reconcile
	err = baseClusterTopology.ApplyResources(ctx, clusterResources, cl)
	if err != nil {
		return stacktrace.Propagate(err, "An error occurred applying the resources")
	}

	return nil
}
