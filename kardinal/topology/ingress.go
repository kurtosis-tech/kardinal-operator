package topology

import (
	"fmt"

	net "k8s.io/api/networking/v1"
)

type Ingress struct {
	ActiveFlowIDs []string       `json:"activeFlowIDs"`
	Ingresses     []*net.Ingress `json:"ingresses"`
}

func (ingress *Ingress) Print() {
	fmt.Println("Ingress")
	fmt.Printf("Active Flow IDs: %v\n", ingress.ActiveFlowIDs)
}
