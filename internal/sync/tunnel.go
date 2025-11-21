package sync

import (
	"fmt"
	"sort"
	"tunnel/internal/runtime"
)

type TunnelConfigRequest struct {
	Config TunnelConfig `json:"config"`
}

type TunnelConfig struct {
	Ingress []TunnelIngressRule `json:"ingress"`
}

type TunnelIngressRule struct {
	Hostname      string         `json:"hostname,omitempty"`
	Service       string         `json:"service"`
	OriginRequest map[string]any `json:"originRequest,omitempty"`
}

// SyncTunnel updates the Cloudflare Tunnel configuration to match the desired state.
func SyncTunnel(runtime *runtime.Runtime, state *SyncState) error {
	ingressRules := make([]TunnelIngressRule, 0)

	for host, service := range state.HostToService {
		ingressRules = append(ingressRules, TunnelIngressRule{
			Hostname: host,
			Service:  service,
		})
	}

	// Sort rules by hostname for consistency, otherwise we might end up with
	// unnecessary config changes on each sync.
	sort.Slice(ingressRules, func(i, j int) bool {
		return ingressRules[i].Hostname < ingressRules[j].Hostname
	})

	ingressRules = append(ingressRules, TunnelIngressRule{
		Service: "http_status:404",
	})

	reqBody := TunnelConfigRequest{
		Config: TunnelConfig{
			Ingress: ingressRules,
		},
	}

	var cfResp map[string]any
	path := fmt.Sprintf("/accounts/%s/cfd_tunnel/%s/configurations", runtime.Config.CloudFlareAccountID, runtime.Config.CloudFlareTunnelID)

	if err := runtime.Client.CloudFlareClient.Put(runtime.Ctx, path, reqBody, &cfResp); err != nil {
		return fmt.Errorf("error while updating tunnel configuration: %w", err)
	}

	return nil
}
