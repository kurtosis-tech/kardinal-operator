package resources

import (
	"context"
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
	gateway "sigs.k8s.io/gateway-api/apis/v1"
)

const (
	BaselineNamespace        = "baseline"
	defaultVersionLabelValue = "baseline"
	trueStr                  = "true"
	kardinalManagedLabelKey  = "kardinal.dev/managed"

	// Thi is a common label used in several applications and recommended by Kubernetes: https://kubernetes.io/docs/concepts/overview/working-with-objects/common-labels/
	appNameKubernetesLabelKey = "app.kubernetes.io/name"
	appLabelKey               = "app"
	versionLabelKey           = "version"

	fieldOwner = "kardinal-operator"
)

type labeledResources interface {
	GetLabels() map[string]string
	SetLabels(labels map[string]string)
	GetName() string
}

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
		metav1.NamespaceDefault,
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

	gateways := &gateway.GatewayList{}
	err = cl.List(ctx, gateways, client.InNamespace(namespace))
	if err != nil {
		return nil, stacktrace.Propagate(err, "An error occurred retrieving the list of gateways for namespace %s", namespace)
	}

	httpRoutes := &gateway.HTTPRouteList{}
	err = cl.List(ctx, httpRoutes, client.InNamespace(namespace))
	if err != nil {
		return nil, stacktrace.Propagate(err, "An error occurred retrieving the list of HTTP routes for namespace %s", namespace)
	}

	envoyFilters := &istioclient.EnvoyFilterList{}
	err = cl.List(ctx, envoyFilters, client.InNamespace(namespace))
	if err != nil {
		return nil, stacktrace.Propagate(err, "An error occurred retrieving the list of envoy filters for namespace %s", namespace)
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
		Gateways:         lo.Map(gateways.Items, func(gateway gateway.Gateway, _ int) *gateway.Gateway { return &gateway }),
		HTTPRoutes:       lo.Map(httpRoutes.Items, func(route gateway.HTTPRoute, _ int) *gateway.HTTPRoute { return &route }),
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

// ApplyResources compares the current cluster resources with the base + flows topology resources and applies the differences.
// getObjectFunc and getObjectsFunc are used to retrieve the namespace resources.
// compareObjectsFunc is used to compare two resources
func ApplyResources(
	ctx context.Context,
	clusterResources *Resources,
	clusterTopologyResources *Resources,
	cl client.Client,
	getObjectsFunc func(namespace *Namespace) []client.Object,
	getObjectFunc func(namespace *Namespace, name string) client.Object,
	compareObjectsFunc func(object1 client.Object, object2 client.Object) bool) error {
	for _, namespace := range clusterResources.Namespaces {
		clusterTopologyNamespace := clusterTopologyResources.GetNamespaceByName(namespace.Name)
		if clusterTopologyNamespace != nil {
			for _, clusterTopologyNamespaceObject := range getObjectsFunc(clusterTopologyNamespace) {
				namespaceObject := getObjectFunc(namespace, clusterTopologyNamespaceObject.GetName())
				if namespaceObject == nil {
					logrus.Infof("Creating %s %s", clusterTopologyNamespaceObject.GetObjectKind().GroupVersionKind().String(), clusterTopologyNamespaceObject.GetName())
					err := cl.Create(ctx, clusterTopologyNamespaceObject, client.FieldOwner(fieldOwner))
					if err != nil {
						return stacktrace.Propagate(err, "An error occurred creating %s %s", clusterTopologyNamespaceObject.GetObjectKind().GroupVersionKind().String(), clusterTopologyNamespaceObject.GetName())
					}
				} else {
					namespaceObjectLabels := namespaceObject.GetLabels()
					// OPERATOR-TODO we have to check if it was marked for deletion by Kubernetes and handle it that situation
					isManaged, found := namespaceObjectLabels[kardinalManagedLabelKey]
					if found && isManaged == trueStr {
						if !compareObjectsFunc(clusterTopologyNamespaceObject, namespaceObject) {
							logrus.Infof("Updating %s %s", clusterTopologyNamespaceObject.GetObjectKind().GroupVersionKind().String(), clusterTopologyNamespaceObject.GetName())
							clusterTopologyNamespaceObject.SetResourceVersion(namespaceObject.GetResourceVersion())
							err := cl.Update(ctx, clusterTopologyNamespaceObject)
							if err != nil {
								return stacktrace.Propagate(err, "An error occurred updating %s %s", clusterTopologyNamespaceObject.GetObjectKind().GroupVersionKind().String(), clusterTopologyNamespaceObject.GetName())
							}
						}
					}
				}
			}
		}

		for _, namespaceObject := range getObjectsFunc(namespace) {
			namespaceObjectLabels := namespaceObject.GetLabels()
			isManaged, found := namespaceObjectLabels[kardinalManagedLabelKey]
			if found && isManaged == trueStr {
				if clusterTopologyNamespace == nil || getObjectFunc(clusterTopologyNamespace, namespaceObject.GetName()) == nil {
					logrus.Infof("Deleting %s %s", namespaceObject.GetObjectKind().GroupVersionKind().String(), namespaceObject.GetName())
					err := cl.Delete(ctx, namespaceObject, client.PropagationPolicy(metav1.DeletePropagationForeground))
					if err != nil {
						return stacktrace.Propagate(err, "An error occurred deleting %s %s", namespaceObject.GetObjectKind().GroupVersionKind().String(), namespaceObject.GetName())
					}
				}
			}
		}
	}

	return nil
}

// OPERATOR-TODO check why we have duplicated values, and make sure to create the filters for the flow services I think right now this generates the duplication because both have same name
func ApplyEnvoyFilterResources(ctx context.Context, clusterResources *Resources, clusterTopologyResources *Resources, cl client.Client) error {
	for _, namespace := range clusterResources.Namespaces {
		clusterTopologyNamespace := clusterTopologyResources.GetNamespaceByName(namespace.Name)
		if clusterTopologyNamespace != nil {
			for _, envoyFilter := range clusterTopologyNamespace.EnvoyFilters {
				namespaceEnvoyFilter := namespace.GetEnvoyFilter(envoyFilter.Name)
				if namespaceEnvoyFilter == nil {
					logrus.Infof("Creating envoy filter %s", envoyFilter.Name)
					err := cl.Create(ctx, envoyFilter)
					if err != nil {
						return stacktrace.Propagate(err, "An error occurred creating envoy filter %s", envoyFilter.Name)
					}
				} else {
					if !reflect.DeepEqual(&namespaceEnvoyFilter.Spec, &envoyFilter.Spec) {
						envoyFilter.ResourceVersion = namespaceEnvoyFilter.ResourceVersion
						err := cl.Update(ctx, envoyFilter)
						if err != nil {
							return stacktrace.Propagate(err, "An error occurred updating envoy filter %s", envoyFilter.Name)
						}
					}
				}
			}
		}

		for _, envoyFilter := range namespace.EnvoyFilters {
			if clusterTopologyNamespace == nil || clusterTopologyNamespace.GetEnvoyFilter(envoyFilter.Name) == nil {
				logrus.Infof("Deleting envoy filter %s", envoyFilter.Name)
				err := cl.Delete(ctx, envoyFilter)
				if err != nil {
					return stacktrace.Propagate(err, "An error occurred deleting envoy filter %s", envoyFilter.Name)
				}
			}
		}
	}

	return nil
}

// OPERATOR-TODO make sure to execute this again once we connect the operator to listen to k8s Deployments and Services events
// OPERATOR-TODO there is another approach we could take, if it doesn't works for all use cases, which is to use MutatingAdmissionWebHooks
// related info for this here: https://book.kubebuilder.io/cronjob-tutorial/webhook-implementation and particularly this https://book.kubebuilder.io/reference/webhook-for-core-types
// for creating and webhook for these core types.
func InjectIstioLabelsInServicesAndDeployments(ctx context.Context, cl client.Client, clusterResources *Resources) error {
	for _, namespace := range clusterResources.Namespaces {
		for _, service := range namespace.Services {
			shouldUpdateLabels := ensureIstioLabelsForResource(service)
			if shouldUpdateLabels {
				if err := cl.Update(ctx, service); err != nil {
					return stacktrace.Propagate(err, "An error occurred adding Istio labels to service '%s'", service.GetName())
				}
			}

		}

		for _, deployment := range namespace.Deployments {
			shouldUpdateLabels := ensureIstioLabelsForResource(deployment)
			if shouldUpdateLabels {
				if err := cl.Update(ctx, deployment); err != nil {
					return stacktrace.Propagate(err, "An error occurred adding Istio labels to deployment '%s'", deployment.GetName())
				}
			}
		}
	}

	return nil
}

func ensureIstioLabelsForResource(resource labeledResources) bool {

	var areLabelsUpdated bool

	labels := resource.GetLabels()
	if labels == nil {
		labels = map[string]string{}
	}

	// The 'app' label
	_, ok := labels[appLabelKey]
	if !ok {
		areLabelsUpdated = true
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
		areLabelsUpdated = true
		labels[versionLabelKey] = defaultVersionLabelValue
	}
	if areLabelsUpdated {
		resource.SetLabels(labels)
	}

	return areLabelsUpdated
}
