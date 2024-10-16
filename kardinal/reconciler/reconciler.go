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

const (
	// Thi is a common label used in several applications and recommended by Kubernetes: https://kubernetes.io/docs/concepts/overview/working-with-objects/common-labels/
	appNameKubernetesLabelKey = "app.kubernetes.io/name"
	appLabelKey               = "app"
	versionLabelKey           = "version"
	defaultVersionLabelValue  = "baseline"
)

type labeledResources interface {
	GetLabels() map[string]string
	SetLabels(labels map[string]string)
	GetName() string
}

func Reconcile(ctx context.Context, cl client.Client) error {
	logrus.Info("Reconciling")

	// Get k8s resources
	logrus.Infof("Get cluster resources")
	clusterResources, err := resources.NewResourcesFromClient(ctx, cl)
	if err != nil {
		return stacktrace.Propagate(err, "An error occurred retrieving the list of resources")
	}

	if err := reconcileIstioLabelsForBaselineServicesAndDeployments(ctx, cl, clusterResources); err != nil {
		return stacktrace.Propagate(err, "An error occurred reconciling the Istio labels")
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

			service := baseClusterTopology.GetServiceByName(namespace.Name, flow.Spec.Service)
			if service == nil {
				return stacktrace.Propagate(err, "An error occurred retrieving base cluster topology service %s in namespace %s", flow.Spec.Service, namespace.Name)
			}
			deployment := resources.GetDeploymentFromName(service.ServiceID, namespace.Deployments)
			deploymentCopy := deep.MustCopy(deployment)
			deploymentCopy.Spec.Template.Spec.Containers[0].Image = flow.Spec.Image
			err = flowTopology.UpdateWithFlow(clusterGraph, flow.Name, service, &deploymentCopy.Spec)
			if err != nil {
				return stacktrace.NewError("An error occurred updating the base cluster topology with flow %s", flow.Name)
			}

			// Replace "baseline" version services with baseClusterTopology versions
			for idx, service := range flowTopology.Services {
				if !service.IsManaged {
					baseService := baseClusterTopology.GetServiceByName(service.Namespace, service.ServiceID)
					if baseService == nil {
						return stacktrace.NewError("An error occurred retrieving the baseline service %s", service.ServiceID)
					}
					flowTopology.Services[idx] = baseService
				}
			}

			// Update service dependencies
			for idx, dependency := range flowTopology.ServiceDependencies {
				if !dependency.Service.IsManaged {
					baseService := baseClusterTopology.GetServiceByName(dependency.Service.Namespace, dependency.Service.ServiceID)
					if baseService == nil {
						return stacktrace.NewError("An error occurred retrieving the baseline service %s for dependency %s", service.ServiceID, dependency.Service.ServiceID)
					}
					flowTopology.ServiceDependencies[idx].Service = baseService
				}
				if !dependency.DependsOnService.IsManaged {
					baseDependsOnService := baseClusterTopology.GetServiceByName(dependency.DependsOnService.Namespace, dependency.DependsOnService.ServiceID)
					if baseDependsOnService == nil {
						return stacktrace.NewError("An error occurred retrieving the baseline service %s for depends on", dependency.DependsOnService.ServiceID)
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

// OPERATOR-TODO make sure to execute this again once we connect the operator to listen to k8s Deployments and Services events
// OPERATOR-TODO there is another approach we could take, if it doesn't works for all use cases, which is to use MutatingAdmissionWebHooks
// related info for this here: https://book.kubebuilder.io/cronjob-tutorial/webhook-implementation and particularly this https://book.kubebuilder.io/reference/webhook-for-core-types
// for creating and webhook for these core types.
func reconcileIstioLabelsForBaselineServicesAndDeployments(ctx context.Context, cl client.Client, clusterResources *resources.Resources) error {
	for _, namespace := range clusterResources.Namespaces {
		for _, service := range namespace.Services {
			ensureIstioLabelsForResource(service)
			if err := cl.Update(ctx, service); err != nil {
				return stacktrace.Propagate(err, "An error occurred adding Istio labels to service '%s'", service.GetName())
			}
		}

		for _, deployment := range namespace.Deployments {
			ensureIstioLabelsForResource(deployment)
			if err := cl.Update(ctx, deployment); err != nil {
				return stacktrace.Propagate(err, "An error occurred adding Istio labels to deployment '%s'", deployment.GetName())
			}
		}
	}
	return nil
}

func ensureIstioLabelsForResource(resource labeledResources) error {

	labels := resource.GetLabels()
	if labels == nil {
		labels = map[string]string{}
	}

	// The 'app' label
	_, ok := labels[appLabelKey]
	if !ok {
		appNameKubernetesLabelValue, ok := labels[appNameKubernetesLabelKey]
		if ok {
			labels[appLabelKey] = appNameKubernetesLabelValue
		} else {
			labels[appLabelKey] = resource.GetName()
		}
	}

	// The 'version' label
	// OPERATOR-TODO how are we going to handle when a non-managed resource already has the "version" label and
	// this value is different from the value needed for managing the baseline traffic
	_, ok = labels[versionLabelKey]
	if !ok {
		labels[versionLabelKey] = defaultVersionLabelValue
	}
	resource.SetLabels(labels)

	return nil
}
