package reconciler

import (
	"context"

	"github.com/kurtosis-tech/stacktrace"
	"github.com/sirupsen/logrus"
	"kardinal.dev/kardinal-operator/kardinal/resources"
	"kardinal.dev/kardinal-operator/kardinal/topology"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func Reconcile(ctx context.Context, cl client.Client) error {
	logrus.Info("Reconciling")

	// Get k8s resources
	namespaceStr := "baseline"
	logrus.Infof("Get cluster resources for namespace %s", namespaceStr)
	namespace, err := resources.GetNamespaceResources(ctx, namespaceStr, cl)
	if err != nil {
		return stacktrace.Propagate(err, "An error occurred retrieving the list of resources for namespace %s", namespaceStr)
	}
	// Generate base cluster topology
	logrus.Info("Generate base cluster topology")
	baseClusterTopology, err := topology.NewClusterTopologyFromResources(namespace.Services, namespace.Deployments, namespaceStr, namespaceStr)
	if err != nil {
		return stacktrace.Propagate(err, "An error occurred generating the base cluster topology")
	}

	// Update base cluster topology with flows
	patches := []*topology.ServicePatch{}
	for _, flow := range namespace.Flows {
		logrus.Infof("Processing flow %s", flow.Name)
		service, err := baseClusterTopology.GetService(flow.Spec.Service)
		if err != nil {
			return stacktrace.Propagate(err, "An error occurred retrieving base cluster topology service %s", flow.Spec.Service)
		}
		deployment := resources.GetDeploymentFromName(service.ServiceID, namespace.Deployments)
		deployment.Spec.Template.Spec.Containers[0].Image = flow.Spec.Image
		patch := &topology.ServicePatch{
			Service:        flow.Spec.Service,
			DeploymentSpec: &deployment.Spec,
		}
		patches = append(patches, patch)

		flowPatch := &topology.FlowPatch{
			FlowId:         flow.GetObjectMeta().GetName(),
			ServicePatches: patches,
		}
		err = baseClusterTopology.UpdateWithFlow(flowPatch)
		if err != nil {
			return stacktrace.Propagate(err, "An error occurred updating the base cluster topology with flow %s", flowPatch.FlowId)
		}
	}

	// Reconcile
	baseClusterTopologyResources, _ := baseClusterTopology.GetResources()
	baseClusterTopologyNamespace := baseClusterTopologyResources[namespaceStr]

	// Create missing resources
	if baseClusterTopologyNamespace != nil {
		for _, service := range baseClusterTopologyNamespace.Services {
			if namespace.GetService(service.Name) == nil {
				logrus.Infof("Creating service %s", service.Name)
				_ = cl.Create(ctx, service)
			}
		}
		for _, deployment := range baseClusterTopologyNamespace.Deployments {
			if namespace.GetDeployment(deployment.Name) == nil {
				logrus.Infof("Creating deployment %s", deployment.Name)
				_ = cl.Create(ctx, deployment)
			}
		}
	}

	// Delete missing resources
	for _, service := range namespace.Services {
		serviceAnnotations := service.Annotations
		isManaged, found := serviceAnnotations["kardinal.dev/managed"]
		if found && isManaged == "true" {
			if baseClusterTopologyNamespace == nil || baseClusterTopologyNamespace.GetService(service.Name) == nil {
				logrus.Infof("Deleting service %s", service.Name)
				_ = cl.Delete(ctx, service)
			}
		}
	}
	for _, deployment := range namespace.Deployments {
		deploymentAnnotations := deployment.Annotations
		isManaged, found := deploymentAnnotations["kardinal.dev/managed"]
		if found && isManaged == "true" {
			if baseClusterTopologyNamespace == nil || baseClusterTopologyNamespace.GetDeployment(deployment.Name) == nil {
				logrus.Infof("Deleting deployment %s", deployment.Name)
				_ = cl.Delete(ctx, deployment)
			}
		}
	}

	return nil
}
