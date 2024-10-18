package topology

import (
	gateway "sigs.k8s.io/gateway-api/apis/v1"
)

type GatewayAndRoutes struct {
	ActiveFlowIDs []string                 `json:"activeFlowIDs"`
	Gateways      []*gateway.Gateway       `json:"gateway"`
	GatewayRoutes []*gateway.HTTPRouteSpec `json:"gatewayRoutes"`
}
