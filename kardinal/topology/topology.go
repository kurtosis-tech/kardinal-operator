package topology

import (
	"context"
	"fmt"
	"strings"

	"github.com/brunoga/deep"
	"github.com/dominikbraun/graph"
	"github.com/kurtosis-tech/stacktrace"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	net "k8s.io/api/networking/v1"
	"kardinal.dev/kardinal-operator/kardinal/resources"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	trueStr                 = "true"
	kardinalManagedLabelKey = "kardinal-managed"
)

type ClusterTopology struct {
	FlowID              string               `json:"flowID"`
	Ingress             *Ingress             `json:"ingress"`
	Services            []*Service           `json:"services"`
	ServiceDependencies []*ServiceDependency `json:"serviceDependencies"`
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

func (clusterTopology *ClusterTopology) GetService(serviceName string, namespace string) (*Service, error) {
	for _, service := range clusterTopology.Services {
		if service.Namespace == namespace && service.ServiceID == serviceName {
			return service, nil
		}
	}

	return nil, stacktrace.NewError("Service %s not found in the list of services", serviceName)
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
			// TODO - this should be done for all non http services and not just the stateful ones
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

func (clusterTopology *ClusterTopology) GetResources() (*resources.Resources, error) {
	namespaces := map[string]*resources.Namespace{}
	managedServices := lo.Filter(clusterTopology.Services, func(service *Service, _ int) bool { return service.IsManaged })

	var namespace *resources.Namespace
	for _, service := range managedServices {
		var found bool
		namespace, found = namespaces[service.Namespace]
		if !found {
			namespaces[service.Namespace] = &resources.Namespace{
				Name:        service.Namespace,
				Services:    []*corev1.Service{},
				Deployments: []*appsv1.Deployment{},
			}
			namespace = namespaces[service.Namespace]
		}
		namespace.Services = append(namespace.Services, service.GetCoreV1Service(service.Namespace))
		namespace.Deployments = append(namespace.Deployments, service.GetAppsV1Deployment(service.Namespace))
	}

	groupedServices := lo.GroupBy(clusterTopology.Services, func(item *Service) ServiceNamespace {
		return ServiceNamespace{ServiceID: item.ServiceID, Namespace: item.Namespace}
	})
	for _, services := range groupedServices {
		if len(services) > 0 {
			// TODO: this assumes service specs didn't change. May we need a new version to ClusterTopology data structure

			// ServiceSpec is nil for external services - don't process anything bc theres nothing to add to the cluster
			if services[0].ServiceSpec == nil {
				continue
			}
			service := services[0]
			virtualService, destinationRule := service.GetVirtualService(services)
			namespace = namespaces[service.Namespace]
			namespace.VirtualServices = append(namespace.VirtualServices, virtualService)
			namespace.DestinationRules = append(namespace.DestinationRules, destinationRule)

			// TODO: Add authz policies
		}
	}

	clusterTopologyResources := &resources.Resources{
		Namespaces: lo.Values(namespaces),
	}
	return clusterTopologyResources, nil
}

func (clusterTopology *ClusterTopology) ApplyResources(ctx context.Context, clusterResources *resources.Resources, cl client.Client) error {
	clusterTopologyResources, err := clusterTopology.GetResources()
	if err != nil {
		return stacktrace.Propagate(err, "An error occurred retrieving the list of resources")
	}

	err = resources.ApplyServiceResources(ctx, clusterResources, clusterTopologyResources, cl)
	if err != nil {
		return stacktrace.Propagate(err, "An error occurred applying the service resources")
	}

	err = resources.ApplyDeploymentResources(ctx, clusterResources, clusterTopologyResources, cl)
	if err != nil {
		return stacktrace.Propagate(err, "An error occurred applying the deployment resources")
	}

	err = resources.ApplyVirtualServiceResources(ctx, clusterResources, clusterTopologyResources, cl)
	if err != nil {
		return stacktrace.Propagate(err, "An error occurred applying the virtual service resources")
	}

	err = resources.ApplyDestinationRuleResources(ctx, clusterResources, clusterTopologyResources, cl)
	if err != nil {
		return stacktrace.Propagate(err, "An error occurred applying the virtual service resources")
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
	clusterTopologyCopy := deep.MustCopy(clusterTopology)
	clusterTopologyCopy.FlowID = flowId
	clusterTopologyCopy.Ingress.ActiveFlowIDs = []string{flowId}
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
	}
	for _, topology := range clusterTopologies {
		mergedClusterTopology.Services = append(mergedClusterTopology.Services, topology.Services...)
		mergedClusterTopology.ServiceDependencies = append(mergedClusterTopology.ServiceDependencies, topology.ServiceDependencies...)
		mergedClusterTopology.Ingress.ActiveFlowIDs = append(mergedClusterTopology.Ingress.ActiveFlowIDs, topology.Ingress.ActiveFlowIDs...)
	}
	mergedClusterTopology.Ingress.ActiveFlowIDs = lo.Uniq(mergedClusterTopology.Ingress.ActiveFlowIDs)
	logrus.Infof("Services length: %d", len(mergedClusterTopology.Services))

	// TODO improve the filtering method, we could implement the `Service.Equal` method to compare and filter the services
	// TODO and inside this method we could use the k8s service marshall method (https://pkg.go.dev/k8s.io/api/core/v1#Service.Marsha) and also the same for other k8s fields
	// TODO it should be faster
	mergedClusterTopology.Services = lo.UniqBy(mergedClusterTopology.Services, func(service *Service) ServiceVersion {
		serviceVersion := ServiceVersion{
			ServiceID: service.ServiceID,
			Namespace: service.Namespace,
			Version:   service.Version,
		}
		return serviceVersion
	})
	logrus.Infof("Services length: %d", len(mergedClusterTopology.Services))
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

func NewClusterTopologyFromResources(
	clusterResources *resources.Resources,
) (*ClusterTopology, error) {
	clusterTopologyServices := []*Service{}
	clusterTopologyServiceDependencies := []*ServiceDependency{}
	var clusterTopologyIngress *Ingress

	for _, resourceNamespace := range clusterResources.Namespaces {
		services, serviceDependencies, err := processServices(resourceNamespace.Services, resourceNamespace.Deployments)
		if err != nil {
			return nil, stacktrace.NewError("an error occurred processing the resource services and deployments")
		}
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
	if len(clusterTopologyIngress.Ingresses) == 0 {
		return nil, stacktrace.NewError("At least one ingress is required")
	}
	if len(clusterTopologyServices) == 0 {
		return nil, stacktrace.NewError("At least one service is required in addition to the ingress service(s)")
	}

	clusterTopology := ClusterTopology{
		Services:            clusterTopologyServices,
		ServiceDependencies: clusterTopologyServiceDependencies,
		Ingress:             clusterTopologyIngress,
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

	// TODO: Use the dependency CRs instead
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
