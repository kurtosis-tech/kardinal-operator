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
	logrus.Infof("Get cluster resources")
	namespaces, err := resources.GetNamespaces(ctx, cl)
	if err != nil {
		return stacktrace.Propagate(err, "An error occurred retrieving the list of resources for namespace %s", namespaceStr)
	}
	// Generate base cluster topology
	logrus.Info("Generate base cluster topology")
	baseClusterTopology, err := topology.NewClusterTopologyFromResources(namespaces, namespaceStr)
	if err != nil {
		return stacktrace.Propagate(err, "An error occurred generating the base cluster topology")
	}

	// Update base cluster topology with flows
	for _, namespace := range namespaces {
		for _, flow := range namespace.Flows {
			logrus.Infof("Processing flow %s", flow.Name)
			service, err := baseClusterTopology.GetService(flow.Spec.Service, namespace.Name)
			if err != nil {
				return stacktrace.Propagate(err, "An error occurred retrieving base cluster topology service %s in namespace %s", flow.Spec.Service, namespace.Name)
			}
			deployment := resources.GetDeploymentFromName(service.ServiceID, namespace.Deployments)
			deployment.Spec.Template.Spec.Containers[0].Image = flow.Spec.Image
			patch := &topology.ServicePatch{
				Namespace:      namespace.Name,
				Service:        flow.Spec.Service,
				DeploymentSpec: &deployment.Spec,
			}
			patches := []*topology.ServicePatch{patch}
			flowPatch := &topology.FlowPatch{
				FlowId:         flow.GetObjectMeta().GetName(),
				ServicePatches: patches,
			}
			err = baseClusterTopology.UpdateWithFlow(flowPatch)
			if err != nil {
				return stacktrace.Propagate(err, "An error occurred updating the base cluster topology with flow %s", flowPatch.FlowId)
			}
		}
	}

	// Reconcile
	err = baseClusterTopology.ApplyResources(ctx, namespaces, cl)
	if err != nil {
		return stacktrace.Propagate(err, "An error occurred applying the resources")
	}

	return nil
}
