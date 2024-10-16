package resources

import (
	"context"
	"reflect"
	"strings"

	"github.com/kurtosis-tech/stacktrace"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	istioclient "istio.io/client-go/pkg/apis/networking/v1alpha3"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	net "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kardinalcorev1 "kardinal.dev/kardinal-operator/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	BaselineNamespace       = "baseline"
	trueStr                 = "true"
	kardinalManagedLabelKey = "kardinal.dev/managed"
)

type Resources struct {
	Namespaces []*Namespace
}

func NewResourcesFromClient(ctx context.Context, cl client.Client) (*Resources, error) {

	namespaces := []*Namespace{}
	coreV1Namespaces := &corev1.NamespaceList{}
	err := cl.List(ctx, coreV1Namespaces)
	if err != nil {
		return nil, stacktrace.Propagate(err, "An error occurred retrieving the list of namespaces")
	}

	namespacePrefixesToIgnore := []string{
		"default",
		"ingress-nginx",
		"istio",
		"kube",
	}
	for _, coreV1Namespace := range coreV1Namespaces.Items {
		ignore := false
		for _, prefix := range namespacePrefixesToIgnore {
			if strings.HasPrefix(coreV1Namespace.Name, prefix) {
				ignore = true
			}
		}
		if ignore {
			continue
		}
		namespace, err := getNamespaceResources(ctx, coreV1Namespace.Name, cl)
		if err != nil {
			return nil, stacktrace.Propagate(err, "An error occurred retrieving the list of namespaces")
		}

		namespaces = append(namespaces, namespace)
	}

	return &Resources{Namespaces: namespaces}, nil
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

	ingresses := &net.IngressList{}
	err = cl.List(ctx, ingresses, client.InNamespace(namespace))
	if err != nil {
		return nil, stacktrace.Propagate(err, "An error occurred retrieving the list of ingresses for namespace %s", namespace)
	}

	virtualServices := &istioclient.VirtualServiceList{}
	err = cl.List(ctx, virtualServices, client.InNamespace(namespace))
	if err != nil {
		return nil, stacktrace.Propagate(err, "An error occurred retrieving the list of virtual services for namespace %s", namespace)
	}

	destinationRules := &istioclient.DestinationRuleList{}
	err = cl.List(ctx, destinationRules, client.InNamespace(namespace))
	if err != nil {
		return nil, stacktrace.Propagate(err, "An error occurred retrieving the list of destination rules for namespace %s", namespace)
	}

	flows := &kardinalcorev1.FlowList{}
	err = cl.List(ctx, flows, client.InNamespace(namespace))
	if err != nil {
		return nil, stacktrace.Propagate(err, "An error occurred retrieving the list of flows for namespace %s", namespace)
	}

	return &Namespace{
		Name:             namespace,
		Services:         lo.Map(services.Items, func(service corev1.Service, _ int) *corev1.Service { return &service }),
		Deployments:      lo.Map(deployments.Items, func(deployment appsv1.Deployment, _ int) *appsv1.Deployment { return &deployment }),
		Ingresses:        lo.Map(ingresses.Items, func(ingress net.Ingress, _ int) *net.Ingress { return &ingress }),
		VirtualServices:  virtualServices.Items,
		DestinationRules: destinationRules.Items,
		Flows:            lo.Map(flows.Items, func(flow kardinalcorev1.Flow, _ int) *kardinalcorev1.Flow { return &flow }),
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

func (resources *Resources) GetNamespaceByName(namespace string) *Namespace {
	for _, resourcesNamespace := range resources.Namespaces {
		if resourcesNamespace.Name == namespace {
			return resourcesNamespace
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

func AddAnnotations(obj *metav1.ObjectMeta, annotations map[string]string) {
	objAnnotations := obj.Annotations
	for key, value := range annotations {
		objAnnotations[key] = value
	}
}

// OPERATOR-TODO: Add create, update and delete global options
// OPERATOR-TODO: Refactor the Apply... functions

func ApplyServiceResources(ctx context.Context, clusterResources *Resources, clusterTopologyResources *Resources, cl client.Client) error {
	for _, namespace := range clusterResources.Namespaces {
		clusterTopologyNamespace := clusterTopologyResources.GetNamespaceByName(namespace.Name)
		if clusterTopologyNamespace != nil {
			for _, service := range clusterTopologyNamespace.Services {
				namespaceService := namespace.GetService(service.Name)
				if namespaceService == nil {
					logrus.Infof("Creating service %s", service.Name)
					err := cl.Create(ctx, service)
					if err != nil {
						return stacktrace.Propagate(err, "An error occurred creating service %s", service.Name)
					}
				} else {
					serviceLabels := service.Labels
					isManaged, found := serviceLabels[kardinalManagedLabelKey]
					if found && isManaged == trueStr {
						if !reflect.DeepEqual(namespaceService.Spec, service.Spec) {
							service.ResourceVersion = namespaceService.ResourceVersion
							err := cl.Update(ctx, service)
							if err != nil {
								return stacktrace.Propagate(err, "An error occurred updating service %s", service.Name)
							}
						}
					}
				}
			}
		}

		for _, service := range namespace.Services {
			serviceLabels := service.Labels
			isManaged, found := serviceLabels[kardinalManagedLabelKey]
			if found && isManaged == trueStr {
				if clusterTopologyNamespace == nil || clusterTopologyNamespace.GetService(service.Name) == nil {
					logrus.Infof("Deleting service %s", service.Name)
					err := cl.Delete(ctx, service)
					if err != nil {
						return stacktrace.Propagate(err, "An error occurred deleting service %s", service.Name)
					}
				}
			}
		}
	}

	return nil
}

func ApplyDeploymentResources(ctx context.Context, clusterResources *Resources, clusterTopologyResources *Resources, cl client.Client) error {
	for _, namespace := range clusterResources.Namespaces {
		clusterTopologyNamespace := clusterTopologyResources.GetNamespaceByName(namespace.Name)
		if clusterTopologyNamespace != nil {
			for _, deployment := range clusterTopologyNamespace.Deployments {
				namespaceDeployment := namespace.GetDeployment(deployment.Name)
				if namespaceDeployment == nil {
					logrus.Infof("Creating deployment %s", deployment.Name)
					err := cl.Create(ctx, deployment)
					if err != nil {
						return stacktrace.Propagate(err, "An error occurred creating deployment %s", deployment.Name)
					}
				} else {
					deploymentLabels := deployment.Labels
					isManaged, found := deploymentLabels[kardinalManagedLabelKey]
					if found && isManaged == trueStr {
						if !reflect.DeepEqual(namespaceDeployment.Spec, deployment.Spec) {
							deployment.ResourceVersion = namespaceDeployment.ResourceVersion
							err := cl.Update(ctx, deployment)
							if err != nil {
								return stacktrace.Propagate(err, "An error occurred updating deployment %s", deployment.Name)
							}
						}
					}
				}
			}
		}

		for _, deployment := range namespace.Deployments {
			deploymentLabels := deployment.Labels
			isManaged, found := deploymentLabels[kardinalManagedLabelKey]
			if found && isManaged == trueStr {
				if clusterTopologyNamespace == nil || clusterTopologyNamespace.GetDeployment(deployment.Name) == nil {
					logrus.Infof("Deleting deployment %s", deployment.Name)
					err := cl.Delete(ctx, deployment)
					if err != nil {
						return stacktrace.Propagate(err, "An error occurred deleting deployment %s", deployment.Name)
					}
				}
			}
			/* else {
				annotationsToAdd := map[string]string{
					"sidecar.istio.io/inject": "true",
					// KARDINAL-TODO: make this a flag to help debugging
					// One can view the logs with: kubeclt logs -f -l app=<serviceID> -n <namespace> -c istio-proxy
					"sidecar.istio.io/componentLogLevel": "lua:info",
				}
				AddAnnotations(&deployment.ObjectMeta, annotationsToAdd)
				err := cl.Update(ctx, deployment)
				if err != nil {
					return stacktrace.Propagate(err, "An error occurred updating deployment %s", deployment.Name)
				}
			} */
		}
	}

	return nil
}

func ApplyVirtualServiceResources(ctx context.Context, clusterResources *Resources, clusterTopologyResources *Resources, cl client.Client) error {
	for _, namespace := range clusterResources.Namespaces {
		clusterTopologyNamespace := clusterTopologyResources.GetNamespaceByName(namespace.Name)
		if clusterTopologyNamespace != nil {
			for _, virtualService := range clusterTopologyNamespace.VirtualServices {
				namespaceVirtualService := namespace.GetVirtualService(virtualService.Name)
				if namespaceVirtualService == nil {
					logrus.Infof("Creating virtual service %s", virtualService.Name)
					err := cl.Create(ctx, virtualService)
					if err != nil {
						return stacktrace.Propagate(err, "An error occurred creating virtual service %s", virtualService.Name)
					}
				} else {
					if !reflect.DeepEqual(&namespaceVirtualService.Spec, &virtualService.Spec) {
						virtualService.ResourceVersion = namespaceVirtualService.ResourceVersion
						err := cl.Update(ctx, virtualService)
						if err != nil {
							return stacktrace.Propagate(err, "An error occurred updating virtual service %s", virtualService.Name)
						}
					}
				}
			}
		}

		for _, virtualService := range namespace.VirtualServices {
			if clusterTopologyNamespace == nil || clusterTopologyNamespace.GetVirtualService(virtualService.Name) == nil {
				logrus.Infof("Deleting virtual service %s", virtualService.Name)
				err := cl.Delete(ctx, virtualService)
				if err != nil {
					return stacktrace.Propagate(err, "An error occurred deleting virtual service %s", virtualService.Name)
				}
			}
		}
	}

	return nil
}

func ApplyDestinationRuleResources(ctx context.Context, clusterResources *Resources, clusterTopologyResources *Resources, cl client.Client) error {
	for _, namespace := range clusterResources.Namespaces {
		clusterTopologyNamespace := clusterTopologyResources.GetNamespaceByName(namespace.Name)
		if clusterTopologyNamespace != nil {
			for _, destinationRule := range clusterTopologyNamespace.DestinationRules {
				namespaceDestinationRule := namespace.GetDestinationRule(destinationRule.Name)
				if namespaceDestinationRule == nil {
					logrus.Infof("Creating destination rule %s", destinationRule.Name)
					err := cl.Create(ctx, destinationRule)
					if err != nil {
						return stacktrace.Propagate(err, "An error occurred creating destination rule %s", destinationRule.Name)
					}
				} else {
					if !reflect.DeepEqual(&namespaceDestinationRule.Spec, &destinationRule.Spec) {
						destinationRule.ResourceVersion = namespaceDestinationRule.ResourceVersion
						err := cl.Update(ctx, destinationRule)
						if err != nil {
							return stacktrace.Propagate(err, "An error occurred updating destination rule %s", destinationRule.Name)
						}
					}
				}
			}
		}

		for _, destinationRule := range namespace.DestinationRules {
			if clusterTopologyNamespace == nil || clusterTopologyNamespace.GetDestinationRule(destinationRule.Name) == nil {
				logrus.Infof("Deleting destination rule %s", destinationRule.Name)
				err := cl.Delete(ctx, destinationRule)
				if err != nil {
					return stacktrace.Propagate(err, "An error occurred deleting destination rule %s", destinationRule.Name)
				}
			}
		}
	}

	return nil
}

func ApplyIngressResources(ctx context.Context, clusterResources *Resources, clusterTopologyResources *Resources, cl client.Client) error {
	for _, namespace := range clusterResources.Namespaces {
		clusterTopologyNamespace := clusterTopologyResources.GetNamespaceByName(namespace.Name)
		if clusterTopologyNamespace != nil {
			for _, ingress := range clusterTopologyNamespace.Ingresses {
				namespaceIngress := namespace.GetService(ingress.Name)
				if namespaceIngress == nil {
					logrus.Infof("Creating ingress %s", ingress.Name)
					err := cl.Create(ctx, ingress)
					if err != nil {
						return stacktrace.Propagate(err, "An error occurred creating ingress %s", ingress.Name)
					}
				} else {
					if !reflect.DeepEqual(namespaceIngress.Spec, ingress.Spec) {
						ingress.ResourceVersion = namespaceIngress.ResourceVersion
						err := cl.Update(ctx, ingress)
						if err != nil {
							return stacktrace.Propagate(err, "An error occurred updating ingress %s", ingress.Name)
						}
					}
				}
			}
		}

		for _, ingress := range namespace.Ingresses {
			if clusterTopologyNamespace == nil || clusterTopologyNamespace.GetIngress(ingress.Name) == nil {
				logrus.Infof("Deleting ingress %s", ingress.Name)
				err := cl.Delete(ctx, ingress)
				if err != nil {
					return stacktrace.Propagate(err, "An error occurred deleting ingress %s", ingress.Name)
				}
			}
		}
	}

	return nil
}
