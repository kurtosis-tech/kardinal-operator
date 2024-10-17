package topology

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/samber/lo"
	"google.golang.org/protobuf/types/known/structpb"
	"istio.io/api/networking/v1alpha3"
	istioclient "istio.io/client-go/pkg/apis/networking/v1alpha3"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

const (
	inboundRequestTraceIDFilter = `
function envoy_on_request(request_handle)
  local headers = request_handle:headers()
  local trace_id = headers:get("x-kardinal-trace-id")
  
  if not trace_id then
    request_handle:respond(
      {[":status"] = "400"},
      "Missing required x-kardinal-trace-id header"
    )
  end
end
`

	outgoingRequestTraceIDFilterTemplate = `
%s

function get_trace_id(headers)
  for _, header_name in ipairs(trace_header_priorities) do
    local trace_id = headers:get(header_name)
    if trace_id then
      return trace_id, header_name
    end
  end

  return nil, nil
end

function envoy_on_request(request_handle)
  local headers = request_handle:headers()
  local trace_id, source_header = get_trace_id(headers)
  local hostname = headers:get(":authority")
  
  if not trace_id then
    request_handle:logWarn("No valid trace ID found in request headers")
    request_handle:respond(
      {[":status"] = "400"},
      "Missing required trace ID header"
    )
    return
  end

  if source_header ~= "x-kardinal-trace-id" then
    request_handle:headers():add("x-kardinal-trace-id", trace_id)
    request_handle:logInfo("Set x-kardinal-trace-id from " .. source_header .. ": " .. trace_id)
  end

  local destination = determine_destination(request_handle, trace_id, hostname)
  request_handle:headers():add("x-kardinal-destination", destination)
end

function determine_destination(request_handle, trace_id, hostname)
  hostname = hostname:match("^([^:]+)")
  local headers, body = request_handle:httpCall(
    "outbound|8080||trace-router.default.svc.cluster.local",
    {
      [":method"] = "GET",
      [":path"] = "/route?trace_id=" .. trace_id .. "&hostname=" .. hostname .. "&baseline_prefix=%s",
      [":authority"] = "trace-router.default.svc.cluster.local"
    },
    "",
    5000
  )
  
  if not headers or headers[":status"] ~= "200" then
    request_handle:logWarn("Failed to determine destination, falling back to baseline")
    return hostname .. "-%s"  -- Fallback to baseline
  end
  
  return body
end
`

	luaFilterType = "type.googleapis.com/envoy.extensions.filters.http.lua.v3.Lua"
)

var TraceHeaderPriorities = []string{
	"x-kardinal-trace-id",   // Our custom header (checked first)
	"x-b3-traceid",          // Zipkin B3
	"x-request-id",          // General request ID, often used for tracing
	"x-cloud-trace-context", // Google Cloud Trace
	"x-amzn-trace-id",       // AWS X-Ray
	"traceparent",           // W3C Trace Context
	"uber-trace-id",         // Jaeger
	"x-datadog-trace-id",    // Datadog
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

func (service *Service) Print() {
	fmt.Printf("Service %s\n", service.ServiceID)
	fmt.Printf("\tNamespace: %s\n", service.Namespace)
	fmt.Printf("\tVersion: %s\n", service.Version)
	fmt.Printf("\tManaged: %t\n", service.IsManaged)
}

func (service *Service) GetCoreV1Service() *corev1.Service {
	coreV1Service := &corev1.Service{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "Service",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      service.ServiceID,
			Namespace: service.Namespace,
			Labels: map[string]string{
				appLabelKey: service.ServiceID,
			},
		},
		Spec: *service.ServiceSpec,
	}

	if service.IsManaged {
		coreV1Service.Labels[kardinalManagedLabelKey] = trueStr
	}

	return coreV1Service
}

func (service *Service) GetAppsV1Deployment(namespace string) *appsv1.Deployment {
	name := service.ServiceID
	if service.IsManaged {
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
				appLabelKey:     service.ServiceID,
				versionLabelKey: service.Version,
			},
		},
		Spec: *service.DeploymentSpec,
	}

	if service.IsManaged {
		deployment.Labels[kardinalManagedLabelKey] = trueStr
	}

	numReplicas := int32(1)
	deployment.Spec.Replicas = int32Ptr(numReplicas)
	deployment.Spec.Selector = &metav1.LabelSelector{
		MatchLabels: map[string]string{
			appLabelKey:     service.ServiceID,
			versionLabelKey: service.Version,
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
			// KARDINAL-TODO: make this a flag to help debugging
			// One can view the logs with: kubeclt logs -f -l app=<serviceID> -n <namespace> -c istio-proxy
			"sidecar.istio.io/componentLogLevel": "lua:info",
		},
		Labels: map[string]string{
			appLabelKey:     service.ServiceID,
			versionLabelKey: service.Version,
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
	version, found := coreV1Service.Labels[versionLabelKey]
	if !found {
		version = coreV1Service.Namespace
	}
	clusterTopologyService := &Service{
		ServiceID:   coreV1Service.Name,
		Namespace:   coreV1Service.Namespace,
		Version:     version,
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

	serviceLabels := coreV1Service.Labels
	isManaged, ok := serviceLabels[kardinalManagedLabelKey]
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

func (service *Service) GetVirtualService(services []*Service) (*istioclient.VirtualService, *istioclient.DestinationRule) {
	httpRoutes := []*v1alpha3.HTTPRoute{}
	tcpRoutes := []*v1alpha3.TCPRoute{}
	destinationRule := service.GetDestinationRule(services)

	for _, svc := range services {
		// KARDINAL-TODO: Support for multiple ports
		servicePort := &svc.ServiceSpec.Ports[0]
		var flowHost *string

		if servicePort.AppProtocol != nil && *servicePort.AppProtocol == "HTTP" {
			httpRoutes = append(httpRoutes, svc.GetHTTPRoute(flowHost))
		} else {
			tcpRoutes = append(tcpRoutes, svc.GetTCPRoute())
		}
	}

	return &istioclient.VirtualService{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "networking.istio.io/v1alpha3",
			Kind:       "VirtualService",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      service.ServiceID,
			Namespace: service.Namespace,
			Annotations: map[string]string{
				"kardinal.dev/managed": trueStr,
			},
		},
		Spec: v1alpha3.VirtualService{
			Http:  httpRoutes,
			Tcp:   tcpRoutes,
			Hosts: []string{service.ServiceID},
		},
	}, destinationRule
}

func (service *Service) GetDestinationRule(services []*Service) *istioclient.DestinationRule {
	// KARDINAL-TODO(shared-annotation) - we could store "shared" versions somewhere so that the pointers are the same
	// if we do that then the render work around isn't necessary
	subsets := lo.UniqBy(
		lo.Map(services, func(svc *Service, _ int) *v1alpha3.Subset {
			newSubset := &v1alpha3.Subset{
				Name: svc.Version,
				Labels: map[string]string{
					versionLabelKey: svc.Version,
				},
			}

			// KARDINAL-TODO Narrow down this configuration to only subsets created for telepresence intercepts or find a way to enable TLS for telepresence intercepts https://github.com/kurtosis-tech/kardinal-kontrol/issues/14
			// This config is necessary for Kardinal/Telepresence (https://www.telepresence.io/) integration
			if svc.IsManaged {
				newTrafficPolicy := &v1alpha3.TrafficPolicy{
					Tls: &v1alpha3.ClientTLSSettings{
						Mode: v1alpha3.ClientTLSSettings_DISABLE,
					},
				}
				newSubset.TrafficPolicy = newTrafficPolicy
			}

			return newSubset
		}),
		func(subset *v1alpha3.Subset) string {
			return subset.Name
		},
	)

	return &istioclient.DestinationRule{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "networking.istio.io/v1alpha3",
			Kind:       "DestinationRule",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      service.ServiceID,
			Namespace: service.Namespace,
		},
		Spec: v1alpha3.DestinationRule{
			Host:    service.ServiceID,
			Subsets: subsets,
		},
	}
}

func (service *Service) GetTCPRoute() *v1alpha3.TCPRoute {
	servicePort := &service.ServiceSpec.Ports[0]
	return &v1alpha3.TCPRoute{
		Match: []*v1alpha3.L4MatchAttributes{{
			Port: uint32(servicePort.Port),
			SourceLabels: map[string]string{
				versionLabelKey: service.Version,
			},
		}},
		Route: []*v1alpha3.RouteDestination{
			{
				Destination: &v1alpha3.Destination{
					Host:   service.ServiceID,
					Subset: service.Version,
					Port: &v1alpha3.PortSelector{
						Number: uint32(servicePort.Port),
					},
				},
				Weight: 100,
			},
		},
	}
}

func (service *Service) GetHTTPRoute(host *string) *v1alpha3.HTTPRoute {
	matches := []*v1alpha3.HTTPMatchRequest{
		{
			Headers: map[string]*v1alpha3.StringMatch{
				"x-kardinal-destination": {
					MatchType: &v1alpha3.StringMatch_Exact{
						Exact: service.ServiceID + "-" + service.Version,
					},
				},
			},
		},
	}

	if host != nil {
		matches = append(matches, &v1alpha3.HTTPMatchRequest{
			Headers: map[string]*v1alpha3.StringMatch{
				"x-kardinal-destination": {
					MatchType: &v1alpha3.StringMatch_Exact{
						Exact: *host + "-" + service.Version,
					},
				},
			},
		})
	}

	return &v1alpha3.HTTPRoute{
		Match: matches,
		Route: []*v1alpha3.HTTPRouteDestination{
			{
				Destination: &v1alpha3.Destination{
					Host:   service.ServiceID,
					Subset: service.Version,
				},
			},
		},
	}
}

func (service *Service) GetVersionedService(flowVersion string, namespace string) *corev1.Service {
	serviceSpecCopy := service.ServiceSpec.DeepCopy()

	serviceSpecCopy.ClusterIP = ""
	serviceSpecCopy.ClusterIPs = []string{}
	serviceSpecCopy.Selector = map[string]string{
		appLabelKey:     service.ServiceID,
		versionLabelKey: service.Version,
	}

	return &corev1.Service{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "Service",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-%s", service.ServiceID, flowVersion),
			Namespace: namespace,
			Labels: map[string]string{
				appLabelKey:             service.ServiceID,
				versionLabelKey:         flowVersion,
				kardinalManagedLabelKey: trueStr,
			},
		},
		Spec: *serviceSpecCopy,
	}
}

// OPERATOR-TODO update this once we include the Kubernetes Gateway API
func (service *Service) GetEnvoyFilters() []*istioclient.EnvoyFilter {
	namespace := service.Namespace

	if !service.IsHTTP() {
		return []*istioclient.EnvoyFilter{}
	}
	inboundFilter := &istioclient.EnvoyFilter{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "networking.istio.io/v1alpha3",
			Kind:       "EnvoyFilter",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-inbound-trace-id-check", service.ServiceID),
			Namespace: namespace,
		},
		Spec: v1alpha3.EnvoyFilter{
			WorkloadSelector: &v1alpha3.WorkloadSelector{
				Labels: map[string]string{
					"app": service.ServiceID,
				},
			},
			ConfigPatches: []*v1alpha3.EnvoyFilter_EnvoyConfigObjectPatch{
				{
					ApplyTo: v1alpha3.EnvoyFilter_HTTP_FILTER,
					Match: &v1alpha3.EnvoyFilter_EnvoyConfigObjectMatch{
						Context: v1alpha3.EnvoyFilter_SIDECAR_INBOUND,
						ObjectTypes: &v1alpha3.EnvoyFilter_EnvoyConfigObjectMatch_Listener{
							Listener: &v1alpha3.EnvoyFilter_ListenerMatch{
								FilterChain: &v1alpha3.EnvoyFilter_ListenerMatch_FilterChainMatch{
									Filter: &v1alpha3.EnvoyFilter_ListenerMatch_FilterMatch{
										Name: "envoy.filters.network.http_connection_manager",
									},
								},
							},
						},
					},
					Patch: &v1alpha3.EnvoyFilter_Patch{
						Operation: v1alpha3.EnvoyFilter_Patch_INSERT_BEFORE,
						Value: &structpb.Struct{
							Fields: map[string]*structpb.Value{
								"name": {Kind: &structpb.Value_StringValue{StringValue: "envoy.lua"}},
								"typed_config": {
									Kind: &structpb.Value_StructValue{
										StructValue: &structpb.Struct{
											Fields: map[string]*structpb.Value{
												"@type":      {Kind: &structpb.Value_StringValue{StringValue: luaFilterType}},
												"inlineCode": {Kind: &structpb.Value_StringValue{StringValue: inboundRequestTraceIDFilter}},
											},
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}

	// the baseline topology (or prod topology) flow ID and flow version and host are equal to the namespace these four should use same value
	baselineHostName := namespace
	outboundFilter := &istioclient.EnvoyFilter{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "networking.istio.io/v1alpha3",
			Kind:       "EnvoyFilter",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-outbound-trace-router", service.ServiceID),
			Namespace: namespace,
		},
		Spec: v1alpha3.EnvoyFilter{
			WorkloadSelector: &v1alpha3.WorkloadSelector{
				Labels: map[string]string{
					"app": service.ServiceID,
				},
			},
			ConfigPatches: []*v1alpha3.EnvoyFilter_EnvoyConfigObjectPatch{
				{
					ApplyTo: v1alpha3.EnvoyFilter_HTTP_FILTER,
					Match: &v1alpha3.EnvoyFilter_EnvoyConfigObjectMatch{
						Context: v1alpha3.EnvoyFilter_SIDECAR_OUTBOUND,
						ObjectTypes: &v1alpha3.EnvoyFilter_EnvoyConfigObjectMatch_Listener{
							Listener: &v1alpha3.EnvoyFilter_ListenerMatch{
								FilterChain: &v1alpha3.EnvoyFilter_ListenerMatch_FilterChainMatch{
									Filter: &v1alpha3.EnvoyFilter_ListenerMatch_FilterMatch{
										Name: "envoy.filters.network.http_connection_manager",
									},
								},
							},
						},
					},
					Patch: &v1alpha3.EnvoyFilter_Patch{
						Operation: v1alpha3.EnvoyFilter_Patch_INSERT_BEFORE,
						Value: &structpb.Struct{
							Fields: map[string]*structpb.Value{
								"name": {Kind: &structpb.Value_StringValue{StringValue: "envoy.lua"}},
								"typed_config": {
									Kind: &structpb.Value_StructValue{
										StructValue: &structpb.Struct{
											Fields: map[string]*structpb.Value{
												"@type":      {Kind: &structpb.Value_StringValue{StringValue: luaFilterType}},
												"inlineCode": {Kind: &structpb.Value_StringValue{StringValue: getOutgoingRequestTraceIDFilter(baselineHostName)}},
											},
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}

	return []*istioclient.EnvoyFilter{inboundFilter, outboundFilter}
}

func getOutgoingRequestTraceIDFilter(baselineHostName string) string {
	return fmt.Sprintf(outgoingRequestTraceIDFilterTemplate, generateLuaTraceHeaderPriorities(), baselineHostName, baselineHostName)
}

func generateLuaTraceHeaderPriorities() string {
	var sb strings.Builder
	sb.WriteString("local trace_header_priorities = {")
	for i, header := range TraceHeaderPriorities {
		if i > 0 {
			sb.WriteString(", ")
		}
		sb.WriteString(fmt.Sprintf("%q", header))
	}
	sb.WriteString("}")
	return sb.String()
}

type ServiceNamespace struct {
	ServiceID string
	Namespace string
}

type ServiceVersion struct {
	ServiceID string
	Namespace string
	Version   string
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

type ServiceDependencyVersion struct {
	ServiceID                 string
	Namespace                 string
	Version                   string
	DependOnServiceID         string
	DependsOnServiceNamespace string
	DependsOnServiceVersion   string
}
