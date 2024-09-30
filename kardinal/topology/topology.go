package topology

import (
	"strings"

	"github.com/kurtosis-tech/stacktrace"
	"github.com/sirupsen/logrus"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	net "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type ClusterTopology struct {
	FlowID              string              `json:"flowID"`
	Ingress             *Ingress            `json:"ingress"`
	Services            []*Service          `json:"services"`
	ServiceDependencies []ServiceDependency `json:"serviceDependencies"`
	Namespace           string              `json:"namespace"`
}

type Service struct {
	ServiceID               string                 `json:"serviceID"`
	Version                 string                 `json:"version"`
	ServiceSpec             *corev1.ServiceSpec    `json:"serviceSpec"`
	DeploymentSpec          *appsv1.DeploymentSpec `json:"deploymentSpec"`
	IsExternal              bool                   `json:"isExternal"`
	IsStateful              bool                   `json:"isStateful"`
	IsShared                bool                   `json:"isShared"`
	OriginalVersionIfShared string                 `json:"originalVersionIfShared"`
}

type ServiceDependency struct {
	Service          *Service            `json:"service"`
	DependsOnService *Service            `json:"dependsOnService"`
	DependencyPort   *corev1.ServicePort `json:"dependencyPort"`
}

type Ingress struct {
	ActiveFlowIDs []string      `json:"activeFlowIDs"`
	Ingresses     []net.Ingress `json:"ingresses"`
}

func NewClusterTopologyFromResources(
	services *corev1.ServiceList,
	deployments *appsv1.DeploymentList,
	namespace string,
	version string,
) (*ClusterTopology, error) {
	clusterTopologyServices, clusterTopologyServiceDependencies, err := processServices(services, deployments, version)
	if err != nil {
		return nil, stacktrace.NewError("an error occurred processing the service configs")
	}

	// some validations
	if len(clusterTopologyServices) == 0 {
		return nil, stacktrace.NewError("At least one service is required in addition to the ingress service(s)")
	}

	clusterTopology := ClusterTopology{}
	clusterTopology.Namespace = namespace
	clusterTopology.Services = clusterTopologyServices
	clusterTopology.ServiceDependencies = clusterTopologyServiceDependencies

	return &clusterTopology, nil
}

func processServices(services *corev1.ServiceList, deployments *appsv1.DeploymentList, version string) ([]*Service, []ServiceDependency, error) {
	clusterTopologyServices := []*Service{}
	clusterTopologyServiceDependencies := []ServiceDependency{}
	externalServicesDependencies := []ServiceDependency{}

	type serviceWithDependenciesAnnotation struct {
		service                *Service
		dependenciesAnnotation string
	}
	serviceWithDependencies := []*serviceWithDependenciesAnnotation{}

	for _, service := range services.Items {
		serviceAnnotations := service.GetObjectMeta().GetAnnotations()

		// 1- Service
		logrus.Infof("Processing service: %v", service.GetObjectMeta().GetName())
		serviceName := service.GetObjectMeta().GetName()
		deployment := getDeploymentFromName(serviceName, deployments)
		clusterTopologyService := newClusterTopologyServiceFromServiceAndDeployment(&service, deployment, version)

		// 2- Service dependencies (creates a list of services with dependencies)
		dependencies, ok := serviceAnnotations["kardinal.dev.service/dependencies"]
		if ok {
			newServiceWithDependenciesAnnotation := &serviceWithDependenciesAnnotation{&clusterTopologyService, dependencies}
			serviceWithDependencies = append(serviceWithDependencies, newServiceWithDependenciesAnnotation)
		}
		clusterTopologyServices = append(clusterTopologyServices, &clusterTopologyService)
	}

	// Set the service dependencies in the clusterTopologyService
	// first iterate on the service with dependencies list
	for _, svcWithDependenciesAnnotation := range serviceWithDependencies {

		serviceAndPorts := strings.Split(svcWithDependenciesAnnotation.dependenciesAnnotation, ",")
		for _, serviceAndPort := range serviceAndPorts {
			serviceAndPortParts := strings.Split(serviceAndPort, ":")
			depService, depServicePort, err := getServiceAndPortFromClusterTopologyServices(serviceAndPortParts[0], serviceAndPortParts[1], clusterTopologyServices)
			if err != nil {
				return nil, nil, stacktrace.Propagate(err, "An error occurred finding the service dependency for service %s and port %s", serviceAndPortParts[0], serviceAndPortParts[1])
			}

			serviceDependency := ServiceDependency{
				Service:          svcWithDependenciesAnnotation.service,
				DependsOnService: depService,
				DependencyPort:   depServicePort,
			}

			clusterTopologyServiceDependencies = append(clusterTopologyServiceDependencies, serviceDependency)
		}
	}
	// then add the external services dependencies
	clusterTopologyServiceDependencies = append(clusterTopologyServiceDependencies, externalServicesDependencies...)

	return clusterTopologyServices, clusterTopologyServiceDependencies, nil
}

func newClusterTopologyServiceFromServiceAndDeployment(service *corev1.Service, deployment *appsv1.Deployment, version string) Service {
	serviceAnnotations := service.GetObjectMeta().GetAnnotations()

	clusterTopologyService := Service{
		ServiceID:      service.GetObjectMeta().GetName(),
		Version:        version,
		ServiceSpec:    &service.Spec,
		DeploymentSpec: &deployment.Spec,
	}
	isStateful, ok := serviceAnnotations["kardinal.dev.service/stateful"]
	if ok && isStateful == "true" {
		clusterTopologyService.IsStateful = true
	}
	isExternal, ok := serviceAnnotations["kardinal.dev.service/external"]
	if ok && isExternal == "true" {
		clusterTopologyService.IsExternal = true
	}

	isShared, ok := serviceAnnotations["kardinal.dev.service/shared"]
	if ok && isShared == "true" {
		clusterTopologyService.IsShared = true
	}
	return clusterTopologyService
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

func getDeploymentFromName(name string, deployments *appsv1.DeploymentList) *appsv1.Deployment {
	for _, deployment := range deployments.Items {
		deploymentName := getObjectName(deployment.GetObjectMeta().(*metav1.ObjectMeta))
		if name == deploymentName {
			return &deployment
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
