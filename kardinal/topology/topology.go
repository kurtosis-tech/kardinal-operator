package topology

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/kurtosis-tech/stacktrace"
	"github.com/sirupsen/logrus"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	net "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"kardinal.dev/kardinal-operator/kardinal/resources"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type ClusterTopology struct {
	FlowID              string               `json:"flowID"`
	Ingress             *Ingress             `json:"ingress"`
	Services            []*Service           `json:"services"`
	ServiceDependencies []*ServiceDependency `json:"serviceDependencies"`
}

func (clusterTopology *ClusterTopology) GetService(serviceName string, namespace string) (*Service, error) {
	for _, service := range clusterTopology.Services {
		if service.Namespace == namespace && service.ServiceID == serviceName {
			return service, nil
		}
	}

	return nil, stacktrace.NewError("Service %s not found in the list of services", serviceName)
}

func (clusterTopology *ClusterTopology) UpdateWithFlow(flowPatch *FlowPatch) error {
	flowID := flowPatch.FlowId

	for _, servicePatch := range flowPatch.ServicePatches {
		targetService, err := clusterTopology.GetService(servicePatch.Service, servicePatch.Namespace)
		if err != nil {
			return err
		}
		modifiedTargetService := DeepCopyService(targetService)
		modifiedTargetService.DeploymentSpec = servicePatch.DeploymentSpec
		modifiedTargetService.Version = flowID
		modifiedTargetService.IsManaged = true
		clusterTopology.Services = append(clusterTopology.Services, modifiedTargetService)
	}

	return nil
}

func (clusterTopology *ClusterTopology) GetResources() (map[string]*resources.Namespace, error) {
	namespaces := map[string]*resources.Namespace{}
	for _, service := range clusterTopology.Services {
		if service.IsManaged {
			namespace, found := namespaces[service.Namespace]
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
	}

	return namespaces, nil
}

func (clusterTopology *ClusterTopology) ApplyResources(ctx context.Context, namespaces []*resources.Namespace, cl client.Client) error {
	clusterTopologyNamespaces, err := clusterTopology.GetResources()
	if err != nil {
		return stacktrace.Propagate(err, "An error occurred retrieving the list of resources")
	}

	for _, namespace := range namespaces {
		clusterTopologyNamespace := clusterTopologyNamespaces[namespace.Name]
		if clusterTopologyNamespace != nil {
			for _, service := range clusterTopologyNamespace.Services {
				if namespace.GetService(service.Name) == nil {
					logrus.Infof("Creating service %s", service.Name)
					_ = cl.Create(ctx, service)
				}
			}
			for _, deployment := range clusterTopologyNamespace.Deployments {
				if namespace.GetDeployment(deployment.Name) == nil {
					logrus.Infof("Creating deployment %s", deployment.Name)
					_ = cl.Create(ctx, deployment)
				}
			}
		}

		for _, service := range namespace.Services {
			serviceAnnotations := service.Annotations
			isManaged, found := serviceAnnotations["kardinal.dev/managed"]
			if found && isManaged == "true" {
				if clusterTopologyNamespace == nil || clusterTopologyNamespace.GetService(service.Name) == nil {
					logrus.Infof("Deleting service %s", service.Name)
					_ = cl.Delete(ctx, service)
				}
			}
		}
		for _, deployment := range namespace.Deployments {
			deploymentAnnotations := deployment.Annotations
			isManaged, found := deploymentAnnotations["kardinal.dev/managed"]
			if found && isManaged == "true" {
				if clusterTopologyNamespace == nil || clusterTopologyNamespace.GetDeployment(deployment.Name) == nil {
					logrus.Infof("Deleting deployment %s", deployment.Name)
					_ = cl.Delete(ctx, deployment)
				}
			}
		}
	}

	return nil
}

type Service struct {
	ServiceID               string                 `json:"serviceID"`
	Namespace               string                 `json:"namespace"`
	Version                 string                 `json:"version"`
	ServiceSpec             *corev1.ServiceSpec    `json:"serviceSpec"`
	DeploymentSpec          *appsv1.DeploymentSpec `json:"deploymentSpec"`
	IsExternal              bool                   `json:"isExternal"`
	IsStateful              bool                   `json:"isStateful"`
	IsShared                bool                   `json:"isShared"`
	OriginalVersionIfShared string                 `json:"originalVersionIfShared"`
	IsManaged               bool                   `json:"isManaged"`
}

func (service *Service) GetCoreV1Service(namespace string) *corev1.Service {
	kardinalManaged := "false"
	if service.Version != namespace {
		kardinalManaged = "true"
	}
	return &corev1.Service{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "Service",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      service.ServiceID,
			Namespace: namespace,
			Labels: map[string]string{
				"app": service.ServiceID,
			},
			Annotations: map[string]string{
				"kardinal.dev/managed": kardinalManaged,
			},
		},
		Spec: *service.ServiceSpec,
	}
}

func (service *Service) GetAppsV1Deployment(namespace string) *appsv1.Deployment {
	kardinalManaged := "false"
	if service.Version != namespace {
		kardinalManaged = "true"
	}
	deployment := appsv1.Deployment{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "apps/v1",
			Kind:       "Deployment",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-%s", service.ServiceID, service.Version),
			Namespace: namespace,
			Labels: map[string]string{
				"app":     service.ServiceID,
				"version": service.Version,
			},
			Annotations: map[string]string{
				"kardinal.dev/managed": kardinalManaged,
			},
		},
		Spec: *service.DeploymentSpec,
	}

	numReplicas := int32(1)
	deployment.Spec.Replicas = int32Ptr(numReplicas)
	deployment.Spec.Selector = &metav1.LabelSelector{
		MatchLabels: map[string]string{
			"app":     service.ServiceID,
			"version": service.Version,
		},
	}
	vol25pct := intstr.FromString("25%")
	deployment.Spec.Strategy = appsv1.DeploymentStrategy{
		Type: appsv1.RollingUpdateDeploymentStrategyType,
		RollingUpdate: &appsv1.RollingUpdateDeployment{
			MaxSurge:       &vol25pct,
			MaxUnavailable: &vol25pct,
		},
	}
	deployment.Spec.Template.ObjectMeta = metav1.ObjectMeta{
		Annotations: map[string]string{
			"sidecar.istio.io/inject": "true",
			// TODO: make this a flag to help debugging
			// One can view the logs with: kubeclt logs -f -l app=<serviceID> -n <namespace> -c istio-proxy
			"sidecar.istio.io/componentLogLevel": "lua:info",
		},
		Labels: map[string]string{
			"app":     service.ServiceID,
			"version": service.Version,
		},
	}

	return &deployment
}

func NewServiceFromServiceAndDeployment(coreV1Service *corev1.Service, deployment *appsv1.Deployment, version string) *Service {
	serviceAnnotations := coreV1Service.Annotations
	namespace := coreV1Service.Namespace

	clusterTopologyService := &Service{
		ServiceID:   coreV1Service.Name,
		Namespace:   namespace,
		Version:     version,
		ServiceSpec: &coreV1Service.Spec,
	}
	if deployment != nil {
		clusterTopologyService.DeploymentSpec = &deployment.Spec
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
	isManaged, ok := serviceAnnotations["kardinal.dev/managed"]
	if ok && isManaged == "true" {
		clusterTopologyService.IsManaged = true
	}
	logrus.Infof("Service %s in namespace %s", clusterTopologyService.ServiceID, clusterTopologyService.Namespace)
	return clusterTopologyService
}

type ServiceHash string

// Hash generates a hash for the Service struct
func (service *Service) Hash() ServiceHash {
	h := sha256.New()

	// Write non-pointer fields directly
	h.Write([]byte(service.ServiceID))
	h.Write([]byte(service.Version))
	h.Write([]byte(fmt.Sprintf("%t", service.IsExternal)))
	h.Write([]byte(fmt.Sprintf("%t", service.IsStateful)))
	h.Write([]byte(fmt.Sprintf("%t", service.IsShared)))
	h.Write([]byte(service.OriginalVersionIfShared))

	// Handle pointer fields
	if service.ServiceSpec != nil {
		serviceSpecJSON, _ := json.Marshal(service.ServiceSpec)
		h.Write(serviceSpecJSON)
	}

	if service.DeploymentSpec != nil {
		deploymentSpecJSON, _ := json.Marshal(service.DeploymentSpec)
		h.Write(deploymentSpecJSON)
	}

	// Return the hex ServiceHash
	hashString := fmt.Sprintf("%x", h.Sum(nil))
	// use custom type to improve API
	return ServiceHash(hashString)
}

type ServiceDependency struct {
	Service          *Service            `json:"service"`
	DependsOnService *Service            `json:"dependsOnService"`
	DependencyPort   *corev1.ServicePort `json:"dependencyPort"`
}

type Ingress struct {
	ActiveFlowIDs []string       `json:"activeFlowIDs"`
	Ingresses     []*net.Ingress `json:"ingresses"`
}

type FlowPatch struct {
	FlowId         string
	ServicePatches []*ServicePatch
}

type ServicePatch struct {
	Namespace      string
	Service        string
	DeploymentSpec *appsv1.DeploymentSpec
}

func NewClusterTopologyFromResources(
	namespaces []*resources.Namespace,
	version string,
) (*ClusterTopology, error) {
	clusterTopologyServices := []*Service{}
	clusterTopologyServiceDependencies := []*ServiceDependency{}

	for _, resourceNamespace := range namespaces {
		services, serviceDependencies, err := processServices(resourceNamespace.Services, resourceNamespace.Deployments, version)
		if err != nil {
			return nil, stacktrace.NewError("an error occurred processing the service configs")
		}
		clusterTopologyServices = append(clusterTopologyServices, services...)
		clusterTopologyServiceDependencies = append(clusterTopologyServiceDependencies, serviceDependencies...)
	}

	// some validations
	if len(clusterTopologyServices) == 0 {
		return nil, stacktrace.NewError("At least one service is required in addition to the ingress service(s)")
	}

	clusterTopology := ClusterTopology{}
	clusterTopology.Services = clusterTopologyServices
	clusterTopology.ServiceDependencies = clusterTopologyServiceDependencies

	return &clusterTopology, nil
}

func processServices(services []*corev1.Service, deployments []*appsv1.Deployment, version string) ([]*Service, []*ServiceDependency, error) {
	clusterTopologyServices := []*Service{}
	clusterTopologyServiceDependencies := []*ServiceDependency{}
	externalServicesDependencies := []*ServiceDependency{}

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
		clusterTopologyService := NewServiceFromServiceAndDeployment(service, deployment, version)

		// 2- Service dependencies (creates a list of services with dependencies)
		dependencies, ok := serviceAnnotations["kardinal.dev.service/dependencies"]
		if ok {
			newServiceWithDependenciesAnnotation := &serviceWithDependenciesAnnotation{clusterTopologyService, dependencies}
			serviceWithDependencies = append(serviceWithDependencies, newServiceWithDependenciesAnnotation)
		}
		clusterTopologyServices = append(clusterTopologyServices, clusterTopologyService)
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

			serviceDependency := &ServiceDependency{
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
