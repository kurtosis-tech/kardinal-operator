package topology

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

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

func (service *Service) Print() {
	fmt.Printf("Service %s\n", service.ServiceID)
	fmt.Printf("\tNamespace: %s\n", service.Namespace)
	fmt.Printf("\tVersion: %s\n", service.Version)
	fmt.Printf("\tManaged: %t\n", service.IsManaged)
}

func (service *Service) GetCoreV1Service(namespace string) *corev1.Service {
	kardinalManaged := "false"
	if service.Version != namespace {
		kardinalManaged = trueStr
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
	name := service.ServiceID
	if service.Version != namespace {
		kardinalManaged = trueStr
		name = fmt.Sprintf("%s-%s", service.ServiceID, service.Version)
	}
	deployment := appsv1.Deployment{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "apps/v1",
			Kind:       "Deployment",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
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
			"sidecar.istio.io/inject": trueStr,
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

func (service *Service) IsHTTP() bool {
	if service == nil || service.ServiceSpec == nil || len(service.ServiceSpec.Ports) == 0 {
		return false
	}
	servicePort := service.ServiceSpec.Ports[0]
	return servicePort.AppProtocol != nil && *servicePort.AppProtocol == "HTTP"
}

func NewServiceFromServiceAndDeployment(coreV1Service *corev1.Service, deployment *appsv1.Deployment) *Service {
	namespace := coreV1Service.Namespace
	labelVersion, found := coreV1Service.Labels["kardinal.dev/version"]
	if !found {
		labelVersion = namespace
	}
	clusterTopologyService := &Service{
		ServiceID:   coreV1Service.Name,
		Namespace:   coreV1Service.Namespace,
		Version:     labelVersion,
		ServiceSpec: &coreV1Service.Spec,
	}
	if deployment != nil {
		clusterTopologyService.DeploymentSpec = &deployment.Spec
	}
	serviceAnnotations := coreV1Service.Annotations
	isStateful, ok := serviceAnnotations["kardinal.dev.service/stateful"]
	if ok && isStateful == trueStr {
		clusterTopologyService.IsStateful = true
	}
	isExternal, ok := serviceAnnotations["kardinal.dev.service/external"]
	if ok && isExternal == trueStr {
		clusterTopologyService.IsExternal = true
	}
	isShared, ok := serviceAnnotations["kardinal.dev.service/shared"]
	if ok && isShared == trueStr {
		clusterTopologyService.IsShared = true
	}
	isManaged, ok := serviceAnnotations["kardinal.dev/managed"]
	if ok && isManaged == trueStr {
		clusterTopologyService.IsManaged = true
	}
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

func (serviceDependency *ServiceDependency) Print() {
	fmt.Println("Dependency")
	fmt.Println("Source")
	serviceDependency.Service.Print()
	fmt.Println("Target")
	serviceDependency.DependsOnService.Print()
}
