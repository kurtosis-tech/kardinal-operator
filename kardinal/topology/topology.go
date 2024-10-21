package topology

import (
	"context"
	"fmt"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"reflect"
	gateway "sigs.k8s.io/gateway-api/apis/v1"
	"strings"

	"github.com/brunoga/deep"
	"github.com/dominikbraun/graph"
	"github.com/kurtosis-tech/stacktrace"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	istioclient "istio.io/client-go/pkg/apis/networking/v1alpha3"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	net "k8s.io/api/networking/v1"
	"kardinal.dev/kardinal-operator/kardinal/resources"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	trueStr                 = "true"
	kardinalManagedLabelKey = "kardinal.dev/managed"
	appLabelKey             = "app"
	versionLabelKey         = "version"
)

type ClusterTopology struct {
	FlowID              string               `json:"flowID"`
	Ingress             *Ingress             `json:"ingress"`
	Services            []*Service           `json:"services"`
	ServiceDependencies []*ServiceDependency `json:"serviceDependencies"`
	GatewayAndRoutes    *GatewayAndRoutes    `json:"gatewayAndRoutes"`
}

func (clusterTopology *ClusterTopology) Print() {
	fmt.Println("Cluster Topology")
	fmt.Printf("Flow ID: %s\n", clusterTopology.FlowID)
	clusterTopology.Ingress.Print()
	for _, service := range clusterTopology.Services {
		service.Print()
	}
	for _, serviceDependency := range clusterTopology.ServiceDependencies {
		serviceDependency.Print()
	}
}

func (clusterTopology *ClusterTopology) GetServiceByName(namespace string, name string) *Service {
	for _, service := range clusterTopology.Services {
		if service.Namespace == namespace && service.ServiceID == name {
			return service
		}
	}

	return nil
}

func (clusterTopology *ClusterTopology) GetServiceByVersion(namespace string, name string, version string) *Service {
	for _, service := range clusterTopology.Services {
		if service.Namespace == namespace && service.ServiceID == name && service.Version == version {
			return service
		}
	}

	return nil
}

func (clusterTopology *ClusterTopology) GetBaselineFlowService(namespace string, name string) *Service {
	for _, service := range clusterTopology.Services {
		if service.Namespace == namespace && service.ServiceID == name && !service.IsManaged {
			return service
		}
	}

	return nil
}

func (clusterTopology *ClusterTopology) UpdateWithFlow(
	clusterGraph graph.Graph[ServiceHash, *Service],
	flowId string,
	targetService *Service,
	deploymentSpec *appsv1.DeploymentSpec,
) error {
	statefulPaths := clusterTopology.FindAllDownstreamStatefulPaths(targetService, clusterGraph)
	statefulServices := make([]*Service, 0)
	for _, path := range statefulPaths {
		statefulServiceHash, found := lo.Last(path)
		if !found {
			return stacktrace.NewError("An error occurred finding the last stateful service hash in path %v", path)
		}
		statefulService, err := clusterGraph.Vertex(statefulServiceHash)
		if err != nil {
			return stacktrace.Propagate(err, "An error occurred getting stateful service vertex from graph")
		}
		statefulServices = append(statefulServices, statefulService)
	}
	statefulServices = lo.Uniq(statefulServices)

	modifiedTargetService := deep.MustCopy(targetService)
	modifiedTargetService.IsManaged = true
	modifiedTargetService.DeploymentSpec = deploymentSpec
	modifiedTargetService.Version = flowId
	err := clusterTopology.UpdateService(modifiedTargetService)
	if err != nil {
		return stacktrace.Propagate(err, "An error occurred getting stateful service vertex from graph")
	}

	for serviceIdx, service := range clusterTopology.Services {
		if lo.Contains(statefulServices, service) {
			// Don't modify the original service
			modifiedService := deep.MustCopy(service)
			modifiedService.Version = flowId
			modifiedService.IsManaged = true

			if !modifiedService.IsStateful {
				panic(fmt.Sprintf("Service %s is not stateful but is in stateful paths", modifiedService.ServiceID))
			}

			clusterTopology.Services[serviceIdx] = modifiedService
			clusterTopology.UpdateDependencies(service, modifiedService)

			// create versioned parents for non http stateful services
			// KARDINAL-TODO - this should be done for all non http services and not just the stateful ones
			// 	every child should be copied; immediate parent duplicated
			// 	if children of non http services support http then our routing will have to be modified
			//  we should treat those http services as non http; a hack could be to remove the appProtocol HTTP marking
			if !modifiedService.IsHTTP() {
				logrus.Infof("Stateful service %s is non http; its parents shall be duplicated", modifiedService.ServiceID)
				parents := clusterTopology.FindImmediateParents(service)
				for _, parent := range parents {
					logrus.Infof("Setting parent service %s version to %s", parent.ServiceID, flowId)
					err = clusterTopology.MoveServiceToVersion(parent, flowId)
					if err != nil {
						return stacktrace.Propagate(err, "An error occurred moving parent service %s to version %s", parent.ServiceID, flowId)
					}
				}
			}
		}
	}

	return nil
}

func (clusterTopology *ClusterTopology) UpdateService(modifiedService *Service) error {
	for idx, service := range clusterTopology.Services {
		if service.Namespace == modifiedService.Namespace && service.ServiceID == modifiedService.ServiceID {
			clusterTopology.Services[idx] = modifiedService
			clusterTopology.UpdateDependencies(service, modifiedService)
			return nil
		}
	}

	return stacktrace.NewError("Service %s not found in the list of services", modifiedService.ServiceID)
}

func (clusterTopology *ClusterTopology) UpdateDependencies(targetService *Service, modifiedService *Service) {
	for ix, dependency := range clusterTopology.ServiceDependencies {
		if dependency.Service == targetService {
			dependency.Service = modifiedService
		}
		if dependency.DependsOnService == targetService {
			dependency.DependsOnService = modifiedService
		}
		clusterTopology.ServiceDependencies[ix] = dependency
	}
}

func (clusterTopology *ClusterTopology) GetNamespaces() []string {
	return lo.Uniq(lo.Map(clusterTopology.Services, func(service *Service, _ int) string { return service.Namespace }))
}

func (clusterTopology *ClusterTopology) GetResources() (*resources.Resources, error) {
	resourceNamespaces := map[string]*resources.Namespace{}
	clusterTopologyNamespaces := clusterTopology.GetNamespaces()
	for _, clusterTopologyNamespace := range clusterTopologyNamespaces {
		resourceNamespaces[clusterTopologyNamespace] = &resources.Namespace{
			Name: clusterTopologyNamespace,
		}
	}

	managedServices := lo.Filter(clusterTopology.Services, func(service *Service, _ int) bool { return service.IsManaged })
	for _, service := range managedServices {
		resourceNamespace := resourceNamespaces[service.Namespace]
		resourceNamespace.Services = append(resourceNamespace.Services, service.GetCoreV1Service())
		resourceNamespace.Deployments = append(resourceNamespace.Deployments, service.GetAppsV1Deployment(service.Namespace))
	}

	groupedServices := lo.GroupBy(clusterTopology.Services, func(item *Service) ServiceNamespace {
		return ServiceNamespace{ServiceID: item.ServiceID, Namespace: item.Namespace}
	})
	for _, services := range groupedServices {
		if len(services) > 0 {
			// KARDINAL-TODO: this assumes service specs didn't change. May we need a new version to ClusterTopology data structure

			// ServiceSpec is nil for external services - don't process anything bc theres nothing to add to the cluster
			if services[0].ServiceSpec == nil {
				continue
			}
			service := services[0]
			virtualService, destinationRule := service.GetVirtualService(services)
			resourceNamespace := resourceNamespaces[service.Namespace]
			resourceNamespace.VirtualServices = append(resourceNamespace.VirtualServices, virtualService)
			resourceNamespace.DestinationRules = append(resourceNamespace.DestinationRules, destinationRule)

			// OPERATOR-TODO: Add authz policies
		}
	}

	frontServices := []*corev1.Service{}
	// OPERATOR-TODO include []istioclient.EnvoyFilter as the third returned value once we add the envoy filters objects
	//routes, frontServices, inboundFrontFilters := clusterTopology.getHttpRoutes()
	routes, frontServicesFromHttpRoutes := clusterTopology.getHttpRoutes()
	groupedRoutes := lo.GroupBy(routes, func(routes *gateway.HTTPRoute) string { return routes.Namespace })
	for namespace, routes := range groupedRoutes {
		resourceNamespace := resourceNamespaces[namespace]
		resourceNamespace.HTTPRoutes = append(resourceNamespace.HTTPRoutes, routes...)
	}
	frontServices = append(frontServices, frontServicesFromHttpRoutes...)

	ingresses, frontServicesFromIngresses := clusterTopology.GetNetIngresses()
	groupedIngresses := lo.GroupBy(ingresses, func(ingress *net.Ingress) string {
		return ingress.Namespace
	})
	for namespace, ingresses := range groupedIngresses {
		resourceNamespace := resourceNamespaces[namespace]
		resourceNamespace.Ingresses = append(resourceNamespace.Ingresses, ingresses...)
	}
	frontServices = append(frontServices, frontServicesFromIngresses...)

	frontServicesToAdd := lo.UniqBy(frontServices, func(service *corev1.Service) string { return service.GetName() })
	for _, frontService := range frontServicesToAdd {
		resourceNamespace := resourceNamespaces[frontService.Namespace]
		resourceNamespace.Services = append(resourceNamespace.Services, frontService)
	}

	gateways := clusterTopology.getGateways()
	groupedGateways := lo.GroupBy(gateways, func(gateway *gateway.Gateway) string {
		return gateway.Namespace
	})
	for namespace, gatewayObj := range groupedGateways {
		resourceNamespace := resourceNamespaces[namespace]
		resourceNamespace.Gateways = append(resourceNamespace.Gateways, gatewayObj...)
	}

	clusterTopologyResources := &resources.Resources{
		Namespaces: lo.Values(resourceNamespaces),
	}
	return clusterTopologyResources, nil
}

func (clusterTopology *ClusterTopology) ApplyResources(ctx context.Context, clusterResources *resources.Resources, cl client.Client) error {
	clusterTopologyResources, err := clusterTopology.GetResources()
	if err != nil {
		return stacktrace.Propagate(err, "An error occurred retrieving the list of resources")
	}

	err = resources.ApplyResources(
		ctx, clusterResources, clusterTopologyResources, cl,
		func(namespace *resources.Namespace) []client.Object {
			return lo.Map(namespace.Services, func(service *corev1.Service, _ int) client.Object { return service })
		},
		func(namespace *resources.Namespace, name string) client.Object {
			service := namespace.GetService(name)
			if service == nil {
				// We have to return nil here so the interface returned is nil and not just the underlying object
				return nil
			} else {
				return service
			}
		},
		func(object1 client.Object, object2 client.Object) bool {
			return reflect.DeepEqual(object1.(*corev1.Service).Spec, object2.(*corev1.Service).Spec)
		},
	)
	if err != nil {
		return stacktrace.Propagate(err, "An error occurred applying the service resources")
	}

	err = resources.ApplyResources(
		ctx, clusterResources, clusterTopologyResources, cl,
		func(namespace *resources.Namespace) []client.Object {
			return lo.Map(namespace.Deployments, func(deployment *appsv1.Deployment, _ int) client.Object { return deployment })
		},
		func(namespace *resources.Namespace, name string) client.Object {
			deployment := namespace.GetDeployment(name)
			if deployment == nil {
				// We have to return nil here so the interface returned is nil and not just the underlying object
				return nil
			}
			return deployment
		},
		func(object1 client.Object, object2 client.Object) bool {
			return reflect.DeepEqual(object1.(*appsv1.Deployment).Spec, object2.(*appsv1.Deployment).Spec)
		},
	)
	if err != nil {
		return stacktrace.Propagate(err, "An error occurred applying the deployment resources")
	}

	err = resources.ApplyResources(
		ctx, clusterResources, clusterTopologyResources, cl,
		func(namespace *resources.Namespace) []client.Object {
			return lo.Map(namespace.VirtualServices, func(virtualService *istioclient.VirtualService, _ int) client.Object { return virtualService })
		},
		func(namespace *resources.Namespace, name string) client.Object {
			virtualService := namespace.GetVirtualService(name)
			if virtualService == nil {
				// We have to return nil here so the interface returned is nil and not just the underlying object
				return nil
			}
			return virtualService
		},
		func(object1 client.Object, object2 client.Object) bool {
			return reflect.DeepEqual(&object1.(*istioclient.VirtualService).Spec, &object2.(*istioclient.VirtualService).Spec)
		},
	)
	if err != nil {
		return stacktrace.Propagate(err, "An error occurred applying the virtual service resources")
	}

	err = resources.ApplyResources(
		ctx, clusterResources, clusterTopologyResources, cl,
		func(namespace *resources.Namespace) []client.Object {
			return lo.Map(namespace.DestinationRules, func(destinationRule *istioclient.DestinationRule, _ int) client.Object { return destinationRule })
		},
		func(namespace *resources.Namespace, name string) client.Object {
			destinationRule := namespace.GetDestinationRule(name)
			if destinationRule == nil {
				// We have to return nil here so the interface returned is nil and not just the underlying object
				return nil
			}
			return destinationRule
		},
		func(object1 client.Object, object2 client.Object) bool {
			return reflect.DeepEqual(&object1.(*istioclient.DestinationRule).Spec, &object2.(*istioclient.DestinationRule).Spec)
		},
	)
	if err != nil {
		return stacktrace.Propagate(err, "An error occurred applying the virtual service resources")
	}

	// OPERATOR-TODO: Apply ingress resources
	/* err = resources.ApplyIngressResources(ctx, clusterResources, clusterTopologyResources, cl)
	if err != nil {
		return stacktrace.Propagate(err, "An error occurred applying the ingress resources")
	}*/

	err = resources.ApplyResources(
		ctx, clusterResources, clusterTopologyResources, cl,
		func(namespace *resources.Namespace) []client.Object {
			return lo.Map(namespace.Gateways, func(gateway *gateway.Gateway, _ int) client.Object { return gateway })
		},
		func(namespace *resources.Namespace, name string) client.Object {
			gateway := namespace.GetGateway(name)
			if gateway == nil {
				// We have to return nil here so the interface returned is nil and not just the underlying object
				return nil
			}
			return gateway
		},
		func(object1 client.Object, object2 client.Object) bool {
			return reflect.DeepEqual(&object1.(*gateway.Gateway).Spec, &object2.(*gateway.Gateway).Spec)
		},
	)
	if err != nil {
		return stacktrace.Propagate(err, "An error occurred applying the gateway resources")
	}

	err = resources.ApplyResources(
		ctx, clusterResources, clusterTopologyResources, cl,
		func(namespace *resources.Namespace) []client.Object {
			return lo.Map(namespace.HTTPRoutes, func(route *gateway.HTTPRoute, _ int) client.Object { return route })
		},
		func(namespace *resources.Namespace, name string) client.Object {
			route := namespace.GetHTTPRoute(name)
			if route == nil {
				// We have to return nil here so the interface returned is nil and not just the underlying object
				return nil
			}
			return route
		},
		func(object1 client.Object, object2 client.Object) bool {
			return reflect.DeepEqual(&object1.(*gateway.HTTPRoute).Spec, &object2.(*gateway.HTTPRoute).Spec)
		},
	)
	if err != nil {
		return stacktrace.Propagate(err, "An error occurred applying the HTTP route resources")
	}

	return nil
}

func (clusterTopology *ClusterTopology) GetGraph() graph.Graph[ServiceHash, *Service] {
	serviceHash := func(service *Service) ServiceHash {
		return service.Hash()
	}
	graph := graph.New(serviceHash, graph.Directed())

	for _, service := range clusterTopology.Services {
		_ = graph.AddVertex(service)
	}

	for _, dependency := range clusterTopology.ServiceDependencies {
		_ = graph.AddEdge(dependency.Service.Hash(), dependency.DependsOnService.Hash())
	}

	return graph
}

func (clusterTopology *ClusterTopology) Copy(flowId string) *ClusterTopology {
	activeFlowIDs := []string{flowId}
	clusterTopologyCopy := deep.MustCopy(clusterTopology)
	clusterTopologyCopy.FlowID = flowId
	clusterTopologyCopy.Ingress.ActiveFlowIDs = activeFlowIDs
	clusterTopologyCopy.GatewayAndRoutes.ActiveFlowIDs = activeFlowIDs
	return clusterTopologyCopy
}

func (clusterTopology *ClusterTopology) FindAllDownstreamStatefulPaths(targetService *Service, clusterGraph graph.Graph[ServiceHash, *Service]) [][]ServiceHash {
	allPaths := make([][]ServiceHash, 0)
	for _, service := range clusterTopology.Services {
		if service.IsStateful {
			paths, err := graph.AllPathsBetween(clusterGraph, targetService.Hash(), service.Hash())
			if err != nil {
				logrus.Infof("Error finding paths between %s and %s: %v", targetService.ServiceID, service.ServiceID, err)
				paths = [][]ServiceHash{}
			}
			allPaths = append(allPaths, paths...)
		}
	}
	return allPaths
}

func (clusterTopology *ClusterTopology) FindImmediateParents(service *Service) []*Service {
	parents := make([]*Service, 0)
	for _, dependency := range clusterTopology.ServiceDependencies {
		if dependency.DependsOnService.Namespace == service.Namespace && dependency.DependsOnService.ServiceID == service.ServiceID {
			parents = append(parents, dependency.Service)
		}
	}
	return parents
}

func (clusterTopology *ClusterTopology) MoveServiceToVersion(service *Service, version string) error {
	// Don't duplicate if its already duplicated
	duplicatedService := deep.MustCopy(service)
	duplicatedService.Version = version
	return clusterTopology.UpdateService(duplicatedService)
}

func (clusterTopology *ClusterTopology) Merge(clusterTopologies []*ClusterTopology) *ClusterTopology {
	mergedClusterTopology := &ClusterTopology{
		FlowID:              "all",
		Services:            deep.MustCopy(clusterTopology.Services),
		ServiceDependencies: deep.MustCopy(clusterTopology.ServiceDependencies),
		Ingress:             deep.MustCopy(clusterTopology.Ingress),
		GatewayAndRoutes:    deep.MustCopy(clusterTopology.GatewayAndRoutes),
	}
	for _, topology := range clusterTopologies {
		mergedClusterTopology.Services = append(mergedClusterTopology.Services, topology.Services...)
		mergedClusterTopology.ServiceDependencies = append(mergedClusterTopology.ServiceDependencies, topology.ServiceDependencies...)
		mergedClusterTopology.Ingress.ActiveFlowIDs = append(mergedClusterTopology.Ingress.ActiveFlowIDs, topology.Ingress.ActiveFlowIDs...)
		mergedClusterTopology.GatewayAndRoutes.ActiveFlowIDs = append(mergedClusterTopology.GatewayAndRoutes.ActiveFlowIDs, topology.GatewayAndRoutes.ActiveFlowIDs...)
	}
	mergedClusterTopology.Ingress.ActiveFlowIDs = lo.Uniq(mergedClusterTopology.Ingress.ActiveFlowIDs)
	mergedClusterTopology.GatewayAndRoutes.ActiveFlowIDs = lo.Uniq(mergedClusterTopology.GatewayAndRoutes.ActiveFlowIDs)
	logrus.Infof("Services length: %d", len(mergedClusterTopology.Services))

	// KARDINAL-TODO improve the filtering method, we could implement the `Service.Equal` method to compare and filter the services and inside this method we could use the k8s service marshall method (https://pkg.go.dev/k8s.io/api/core/v1#Service.Marsha) and also the same for other k8s fields it should be faster
	mergedClusterTopology.Services = lo.UniqBy(mergedClusterTopology.Services, func(service *Service) ServiceVersion {
		serviceVersion := ServiceVersion{
			ServiceID: service.ServiceID,
			Namespace: service.Namespace,
			Version:   service.Version,
		}
		return serviceVersion
	})
	mergedClusterTopology.ServiceDependencies = lo.UniqBy(mergedClusterTopology.ServiceDependencies, func(serviceDependency *ServiceDependency) ServiceDependencyVersion {
		serviceDependencyVersion := ServiceDependencyVersion{
			ServiceID:                 serviceDependency.Service.ServiceID,
			Namespace:                 serviceDependency.Service.Namespace,
			Version:                   serviceDependency.Service.Version,
			DependOnServiceID:         serviceDependency.DependsOnService.ServiceID,
			DependsOnServiceNamespace: serviceDependency.DependsOnService.Namespace,
			DependsOnServiceVersion:   serviceDependency.DependsOnService.Version,
		}
		return serviceDependencyVersion
	})

	return mergedClusterTopology
}

// We assume that net.Ingress objects have a namespace defined
func (clusterTopology *ClusterTopology) GetNetIngresses() ([]*net.Ingress, []*corev1.Service) {
	ingressList := []*net.Ingress{}
	frontServices := map[string]*corev1.Service{}

	for _, ingressSpecOriginal := range clusterTopology.Ingress.Ingresses {
		ingressDefinition := ingressSpecOriginal.DeepCopy()
		namespace := ingressDefinition.Namespace
		newRules := []net.IngressRule{}

		for _, ruleOriginal := range ingressDefinition.Spec.Rules {
			for _, activeFlowID := range clusterTopology.Ingress.ActiveFlowIDs {
				logrus.Infof("Setting gateway route for active flow ID: %v", activeFlowID)
				newPaths := []net.HTTPIngressPath{}
				rule := ruleOriginal.DeepCopy()
				flowHostname := replaceOrAddSubdomain(rule.Host, activeFlowID)
				rule.Host = flowHostname

				for _, pathOriginal := range ruleOriginal.HTTP.Paths {
					target := clusterTopology.GetServiceByVersion(namespace, pathOriginal.Backend.Service.Name, activeFlowID)
					// fallback to baseline if backend not found at the active flow
					if target == nil {
						target = clusterTopology.GetBaselineFlowService(namespace, pathOriginal.Backend.Service.Name)
					}
					if target != nil {
						path := *pathOriginal.DeepCopy()
						idVersion := fmt.Sprintf("%s-%s", target.ServiceID, activeFlowID)
						_, serviceAlreadyAdded := frontServices[idVersion]
						if !serviceAlreadyAdded {
							frontServices[idVersion] = target.GetVersionedService(activeFlowID, namespace)
							path.Backend.Service.Name = idVersion
							newPaths = append(newPaths, path)
						}
					} else {
						logrus.Errorf("Backend service %s for Ingress %s not found", pathOriginal.Backend.Service.Name, ingressDefinition.Name)
					}
				}
				rule.HTTP.Paths = newPaths
				newRules = append(newRules, *rule)
			}
		}

		ingressDefinition.Spec.Rules = newRules

		if ingressDefinition.Namespace == "" {
			ingressDefinition.Namespace = namespace
		}

		ingressList = append(ingressList, ingressDefinition)
	}

	return ingressList, lo.Values(frontServices)
}

func (clusterTopology *ClusterTopology) getGateways() []*gateway.Gateway {
	return lo.Map(clusterTopology.GatewayAndRoutes.Gateways, func(gateway *gateway.Gateway, gwId int) *gateway.Gateway {
		if gateway.Namespace == "" {
			gateway.Namespace = metav1.NamespaceDefault
		}
		return gateway
	})
}

// OPERATOR-TODO include []istioclient.EnvoyFilter as the third returned value once we add the envoy filters objects
func (clusterTopology *ClusterTopology) getHttpRoutes() ([]*gateway.HTTPRoute, []*corev1.Service) {
	routes := []*gateway.HTTPRoute{}
	frontServices := map[string]*corev1.Service{}

	// OPERATOR-TODO include []istioclient.EnvoyFilter as the third returned value once we add the envoy filters objects
	//filters := []istioclient.EnvoyFilter{}

	for _, activeFlowID := range clusterTopology.GatewayAndRoutes.ActiveFlowIDs {
		logrus.Infof("Setting gateway route for active flow ID: %v", activeFlowID)
		for routeId, routeOriginal := range clusterTopology.GatewayAndRoutes.GatewayRoutes {
			namespace := routeOriginal.Namespace
			routeSpecOriginal := routeOriginal.Spec
			routeSpec := routeSpecOriginal.DeepCopy()

			routeSpec.Hostnames = lo.Map(routeSpec.Hostnames, func(hostname gateway.Hostname, _ int) gateway.Hostname {
				return gateway.Hostname(replaceOrAddSubdomain(string(hostname), activeFlowID))
			})

			for _, rule := range routeSpec.Rules {
				for refIx, ref := range rule.BackendRefs {
					originalServiceName := string(ref.Name)
					target := clusterTopology.GetServiceByVersion(namespace, originalServiceName, activeFlowID)
					// fallback to baseline if backend not found at the active flow
					if target == nil {
						target = clusterTopology.GetBaselineFlowService(namespace, originalServiceName)
					}
					if target != nil {
						idVersion := fmt.Sprintf("%s-%s", target.ServiceID, activeFlowID)
						_, serviceAlreadyAdded := frontServices[idVersion]
						if !serviceAlreadyAdded {
							frontServices[idVersion] = target.GetVersionedService(activeFlowID, namespace)
							ref.Name = gateway.ObjectName(idVersion)
							//OPERATOR-TODO creo que tambien el problema es que hay 2 http route para el baseline flow
							rule.BackendRefs[refIx] = ref

							// OPERATOR-TODO include []istioclient.EnvoyFilter as the third returned value once we add the envoy filters objects
							//hostnames := lo.Map(routeSpec.Hostnames, func(item gateway.Hostname, _ int) string { return string(item) })

							// Set Envoy FIlter for the service
							//filter := &externalInboudFilter{
							//	filter: generateDynamicLuaScript(allServices, activeFlowID, namespace, hostnames),
							//	name:   strings.Join(hostnames, "-"),
							//}
							//inboundFilter := getInboundFilter(target.ServiceID, namespace, -1, &target.Version, filter)
							//logrus.Debugf("Adding inbound filter to setup routing table for flow '%s' on service '%s', version '%s'", activeFlowID, target.ServiceID, target.Version)
							//filters = append(filters, inboundFilter)
						}
					} else {
						logrus.Errorf(">> service not found %v", ref.Name)
					}
				}
			}

			for parentRefIx, parentRef := range routeSpec.ParentRefs {
				if parentRef.Namespace == nil || string(*parentRef.Namespace) == "" {
					defaultNS := gateway.Namespace(namespace)
					parentRef.Namespace = &defaultNS
				}
				routeSpec.ParentRefs[parentRefIx] = parentRef
			}

			route := &gateway.HTTPRoute{
				TypeMeta: metav1.TypeMeta{
					APIVersion: "gateway.networking.k8s.io/v1",
					Kind:       "HTTPRoute",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      fmt.Sprintf("http-route-%d-%s", routeId, activeFlowID),
					Namespace: namespace,
					Labels: map[string]string{
						kardinalManagedLabelKey: trueStr,
					},
				},
				Spec: *routeSpec,
			}
			routes = append(routes, route)
		}
	}

	// OPERATOR-TODO include []istioclient.EnvoyFilter as the third returned value once we add the envoy filters objects
	//return routes, lo.Values(frontServices), filters
	return routes, lo.Values(frontServices)
}

func NewClusterTopologyFromResources(
	clusterResources *resources.Resources,
) (*ClusterTopology, error) {
	clusterTopologyServices := []*Service{}
	clusterTopologyServiceDependencies := []*ServiceDependency{}
	var clusterTopologyGatewayAndRoutes *GatewayAndRoutes
	var clusterTopologyIngress *Ingress

	for _, resourceNamespace := range clusterResources.Namespaces {
		services, serviceDependencies, err := processServices(resourceNamespace.Services, resourceNamespace.Deployments)
		if err != nil {
			return nil, stacktrace.NewError("an error occurred processing the resource services and deployments")
		}
		clusterTopologyGatewayAndRoutes = processGatewayAndRouteConfigs(resourceNamespace.Gateways, resourceNamespace.HTTPRoutes)
		ingress := processIngresses(resourceNamespace.Ingresses)
		clusterTopologyServices = append(clusterTopologyServices, services...)
		clusterTopologyServiceDependencies = append(clusterTopologyServiceDependencies, serviceDependencies...)
		if len(ingress.Ingresses) > 0 {
			if clusterTopologyIngress != nil {
				return nil, stacktrace.NewError("More than one namespace has ingresses")
			}
			clusterTopologyIngress = ingress
		}
	}

	// some validations
	if len(clusterTopologyIngress.Ingresses) == 0 && len(clusterTopologyGatewayAndRoutes.Gateways) == 0 && len(clusterTopologyGatewayAndRoutes.GatewayRoutes) == 0 {
		return nil, stacktrace.NewError("At least one ingress or gateway is required")
	}
	if len(clusterTopologyServices) == 0 {
		return nil, stacktrace.NewError("At least one service is required in addition to the ingress service(s)")
	}

	clusterTopology := ClusterTopology{
		Services:            clusterTopologyServices,
		ServiceDependencies: clusterTopologyServiceDependencies,
		Ingress:             clusterTopologyIngress,
		GatewayAndRoutes:    clusterTopologyGatewayAndRoutes,
	}

	return &clusterTopology, nil
}

func processServices(services []*corev1.Service, deployments []*appsv1.Deployment) ([]*Service, []*ServiceDependency, error) {
	clusterTopologyServices := []*Service{}
	clusterTopologyServiceDependencies := []*ServiceDependency{}

	type serviceWithDependenciesAnnotation struct {
		service                *Service
		dependenciesAnnotation string
	}
	serviceWithDependencies := []*serviceWithDependenciesAnnotation{}

	for _, service := range services {
		if resources.IsManaged(&service.ObjectMeta) {
			continue
		}

		serviceAnnotations := service.GetObjectMeta().GetAnnotations()

		// 1- Service
		serviceName := service.GetObjectMeta().GetName()
		deployment := resources.GetDeploymentFromName(serviceName, deployments)
		clusterTopologyService := NewServiceFromServiceAndDeployment(service, deployment)

		// 2- Service dependencies (creates a list of services with dependencies)
		dependencies, ok := serviceAnnotations["kardinal.dev.service/dependencies"]
		if ok {
			newServiceWithDependenciesAnnotation := &serviceWithDependenciesAnnotation{clusterTopologyService, dependencies}
			serviceWithDependencies = append(serviceWithDependencies, newServiceWithDependenciesAnnotation)
		}
		clusterTopologyServices = append(clusterTopologyServices, clusterTopologyService)
	}

	// OPERATOR-TODO: Use the dependency CRs instead
	for _, svcWithDependenciesAnnotation := range serviceWithDependencies {

		serviceAndPorts := strings.Split(svcWithDependenciesAnnotation.dependenciesAnnotation, ",")
		for _, serviceAndPort := range serviceAndPorts {
			serviceAndPortParts := strings.Split(serviceAndPort, ":")
			depService, depServicePort, err := getServiceAndPortFromClusterTopologyServices(serviceAndPortParts[0], serviceAndPortParts[1], clusterTopologyServices)
			if err != nil {
				return nil, nil, stacktrace.Propagate(err, "An error occurred finding the service dependency for service %s and port %s", serviceAndPortParts[0], serviceAndPortParts[1])
			}

			serviceDependency := &ServiceDependency{
				Service:          svcWithDependenciesAnnotation.service,
				DependsOnService: depService,
				DependencyPort:   depServicePort,
			}

			clusterTopologyServiceDependencies = append(clusterTopologyServiceDependencies, serviceDependency)
		}
	}

	return clusterTopologyServices, clusterTopologyServiceDependencies, nil
}

func getServiceAndPortFromClusterTopologyServices(serviceName string, servicePortName string, clusterTopologyServices []*Service) (*Service, *corev1.ServicePort, error) {
	for _, service := range clusterTopologyServices {
		if service.ServiceID == serviceName {
			for _, port := range service.ServiceSpec.Ports {
				if port.Name == servicePortName {
					return service, &port, nil
				}
			}
		}
	}

	return nil, nil, stacktrace.NewError("Service %s and Port %s not found in the list of services", serviceName, servicePortName)
}

func processIngresses(ingresses []*net.Ingress) *Ingress {
	clusterTopologyIngress := &Ingress{
		ActiveFlowIDs: []string{resources.BaselineNamespace},
		Ingresses:     []*net.Ingress{},
	}
	for _, ingress := range ingresses {
		ingressAnnotations := ingress.GetObjectMeta().GetAnnotations()

		// Ingress?
		isIngress, ok := ingressAnnotations["kardinal.dev.service/ingress"]
		if ok && isIngress == trueStr {
			clusterTopologyIngress.Ingresses = append(clusterTopologyIngress.Ingresses, ingress)
		}
	}
	return clusterTopologyIngress
}

func processGatewayAndRouteConfigs(gateways []*gateway.Gateway, routes []*gateway.HTTPRoute) *GatewayAndRoutes {
	gatewayAndRoutes := &GatewayAndRoutes{
		ActiveFlowIDs: []string{resources.BaselineNamespace},
		Gateways:      []*gateway.Gateway{},
		GatewayRoutes: []*gateway.HTTPRoute{},
	}
	for _, gatewayObj := range gateways {
		gatewayAnnotations := gatewayObj.GetObjectMeta().GetAnnotations()
		isGateway, ok := gatewayAnnotations["kardinal.dev.service/gateway"]
		if ok && isGateway == "true" {
			if gatewayObj.Spec.Listeners == nil {
				logrus.Warnf("Gateway %v is missing listeners", gatewayObj.Name)
			} else {
				for _, listener := range gatewayObj.Spec.Listeners {
					if listener.Hostname != nil && !strings.HasPrefix(string(*listener.Hostname), "*.") {
						logrus.Warnf("Gateway %v listener %v is missing a wildcard, creating flow entry points will not work properly.", gatewayObj.Name, listener.Hostname)
					}
				}
			}
			logrus.Infof("Managing gateway: %v", gatewayObj.Name)
			gatewayAndRoutes.Gateways = append(gatewayAndRoutes.Gateways, gatewayObj)
		} else {
			logrus.Infof("Gateway %v is not a Kardinal gateway", gatewayObj.Name)
		}
	}
	for _, route := range routes {
		routeAnnotations := route.GetObjectMeta().GetAnnotations()
		isRoute, ok := routeAnnotations["kardinal.dev.service/route"]
		if ok && isRoute == "true" {
			gatewayAndRoutes.GatewayRoutes = append(gatewayAndRoutes.GatewayRoutes, route)
		}
	}
	return gatewayAndRoutes
}
