package resources

import (
	"context"

	"github.com/kurtosis-tech/stacktrace"
	"github.com/samber/lo"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kardinalcorev1 "kardinal.dev/kardinal-operator/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func GetNamespaceResources(ctx context.Context, namespace string, cl client.Client) (*Namespace, error) {

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

	flows := &kardinalcorev1.FlowList{}
	err = cl.List(ctx, flows, client.InNamespace(namespace))
	if err != nil {
		return nil, stacktrace.Propagate(err, "An error occurred retrieving the list of flows for namespace %s", namespace)
	}

	return &Namespace{
		Services:    lo.Map(services.Items, func(service corev1.Service, _ int) *corev1.Service { return &service }),
		Deployments: lo.Map(deployments.Items, func(deployment appsv1.Deployment, _ int) *appsv1.Deployment { return &deployment }),
		Flows:       lo.Map(flows.Items, func(flow kardinalcorev1.Flow, _ int) *kardinalcorev1.Flow { return &flow }),
	}, nil
}

func GetDeploymentFromName(name string, deployments []*appsv1.Deployment) *appsv1.Deployment {
	for _, deployment := range deployments {
		deploymentName := getObjectName(deployment.GetObjectMeta().(*metav1.ObjectMeta))
		if name == deploymentName {
			return deployment
		}
	}
	return nil
}

// Use in priority the label app value
func getObjectName(obj *metav1.ObjectMeta) string {
	labelApp, ok := obj.GetLabels()["app"]
	if ok {
		return labelApp
	}

	return obj.GetName()
}
