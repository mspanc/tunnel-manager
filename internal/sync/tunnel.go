package sync

import (
	"fmt"
	"sort"
	"tunnel/internal/runtime"
)

type tunnelConfigRequest struct {
	Config tunnelConfig `json:"config"`
}

type tunnelConfig struct {
	Ingress []tunnelIngressRule `json:"ingress"`
}

type tunnelIngressRule struct {
	Hostname      string         `json:"hostname,omitempty"`
	Service       string         `json:"service"`
	OriginRequest map[string]any `json:"originRequest,omitempty"`
}

// SyncTunnel updates the Cloudflare Tunnel configuration to match the desired state.
func SyncTunnel(runtime *runtime.Runtime, state *SyncState) error {
	ingressRules := make([]tunnelIngressRule, 0)

	for host, service := range state.HostToService {
		ingressRules = append(ingressRules, tunnelIngressRule{
			Hostname: host,
			Service:  service,
		})
	}

	// Sort rules by hostname for consistency, otherwise we might end up with
	// unnecessary config changes on each sync.
	sort.Slice(ingressRules, func(i, j int) bool {
		return ingressRules[i].Hostname < ingressRules[j].Hostname
	})

	ingressRules = append(ingressRules, tunnelIngressRule{
		Service: "http_status:404",
	})

	reqBody := tunnelConfigRequest{
		Config: tunnelConfig{
			Ingress: ingressRules,
		},
	}

	var resp map[string]any
	path := fmt.Sprintf("/accounts/%s/cfd_tunnel/%s/configurations", runtime.Config.CloudFlareAccountID, runtime.Config.CloudFlareTunnelID)

	if err := runtime.Client.CloudFlareClient.Put(runtime.Ctx, path, reqBody, &resp); err != nil {
		return fmt.Errorf("error while updating tunnel configuration: %w", err)
	}

	return nil
}
