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
	logrus.Info("Flow reconcile")
	// Get k8s resources
	namespaceStr := "baseline"
	namespace, err := resources.GetNamespaceResources(ctx, namespaceStr, cl)
	if err != nil {
		return stacktrace.Propagate(err, "An error occurred retrieving the list of resources for namespace %s", namespaceStr)
	}
	// Generate base cluster topology
	baseClusterTopology, err := topology.NewClusterTopologyFromResources(namespace.Services, namespace.Deployments, namespaceStr, namespaceStr)
	if err != nil {
		return stacktrace.Propagate(err, "An error occurred generating the base cluster topology")
	}

	// Update base cluster topology with flows
	patches := []topology.ServicePatch{}
	for _, flow := range namespace.Flows.Items {
		logrus.Infof("Processing flow %s", flow.Name)
		service, err := baseClusterTopology.GetService(flow.Spec.Service)
		if err != nil {
			return stacktrace.Propagate(err, "An error occurred retrieving base cluster topology service %s", flow.Spec.Service)
		}
		deployment := resources.GetDeploymentFromName(service.ServiceID, namespace.Deployments)
		deployment.Spec.Template.Spec.Containers[0].Image = flow.Spec.Image
		patch := topology.ServicePatch{
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
	for _, service := range baseClusterTopology.Services {
		logrus.Infof("Service %s", service.ServiceID)
		if service.Version == namespaceStr {
			continue
		}
		// TODO: Check for existence and add error check
		corev1Service := service.GetCoreV1Service(namespaceStr)
		_ = cl.Create(ctx, corev1Service)
		appsv1Deployment := service.GetAppsV1Deployment(namespaceStr)
		_ = cl.Create(ctx, appsv1Deployment)
	}

	return nil
}
