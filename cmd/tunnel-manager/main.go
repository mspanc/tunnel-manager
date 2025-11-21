package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
	"tunnel/internal/client"
	"tunnel/internal/config"
	"tunnel/internal/slogf"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type TunnelConfigRequest struct {
	Config TunnelConfig `json:"config"`
}

type TunnelConfig struct {
	Ingress []IngressRule `json:"ingress"`
}

type IngressRule struct {
	Hostname      string                 `json:"hostname,omitempty"`
	Service       string                 `json:"service"`
	OriginRequest map[string]interface{} `json:"originRequest,omitempty"`
}

func main() {
	config, err := config.LoadConfig()
	if err != nil {
		slogf.Errorf("Fatal error: failed to load config: %v", err)
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: config.LogLevel,
	}))

	config.Print(logger)

	client, err := client.NewClient(config)
	if err != nil {
		slogf.Errorf("Fatal error: failed to create clients: %v", err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	slogf.Infof("starting tunnel sync loop")

	// Initial sync
	if err := runSync(ctx, client, config); err != nil {
		slogf.Warnf("initial sync failed: %v", err)
	}

	// Schedule next iteration only after the previous one completes
	for {
		select {
		case <-ctx.Done():
			slogf.Infof("shutting down")
			return
		case <-time.After(config.SyncInterval):
			if err := runSync(ctx, client, config); err != nil {
				slogf.Warnf("sync failed: %v", err)
			}
		}
	}
}

func runSync(
	ctx context.Context,
	client *client.Client,
	config *config.Config,
) error {
	slogf.Infof("running sync")

	nsList, err := client.KubeClient.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("failed to list namespaces: %w", err)
	}

	slogf.Debugf("listing namespaces")
	var namespaces []string
	for _, ns := range nsList.Items {
		namespaces = append(namespaces, ns.Name)
	}
	sort.Strings(namespaces)
	slogf.Debugf("listed namespaces: %v", namespaces)

	var ingressRules []IngressRule

	for _, nsName := range namespaces {
		slogf.Debugf("traversing namespace %s", nsName)
		svcList, err := client.KubeClient.CoreV1().Services(nsName).List(ctx, metav1.ListOptions{})
		if err != nil {
			slogf.Warnf("failed to list services in namespace %q: %v", nsName, err)
			continue
		}

		for _, svc := range svcList.Items {
			slogf.Debugf("traversing service %s/%s: %v", nsName, svc.Name, svc)
			hostnamesStr, ok := svc.Annotations[config.ServiceHostnamesAnnotation]
			if !ok || strings.TrimSpace(hostnamesStr) == "" {
				slogf.Debugf("traversing service %s/%s: missing hostnames annotation, skipping", nsName, svc.Name)
				continue
			}

			// Determine upstream port:
			// 1) Check SERVICE_UPSTREAM_PORT_LABEL (default: cloudflare-tunnel-upstream-port)
			// 2) Fall back to first exposed port
			// 3) If none -> skip service with warning
			port := chooseServicePort(&svc, config.ServiceUpstreamPortAnnotation)
			if port == 0 {
				slogf.Infof("service %s/%s has no usable port; skipping", nsName, svc.Name)
				continue
			}

			serviceFQDN := fmt.Sprintf("%s.%s.svc.cluster.local", svc.Name, nsName)
			serviceURL := fmt.Sprintf("http://%s:%d", serviceFQDN, port)
			slogf.Debugf("traversing service %s/%s: serviceURL = %s", nsName, svc.Name, serviceURL)

			// Domains may be comma- and/or space-separated.
			raw := strings.ReplaceAll(hostnamesStr, ",", " ")
			rawDomains := strings.Fields(raw)
			for _, d := range rawDomains {
				host := strings.TrimSpace(d)
				if host == "" {
					continue
				}

				ingressRules = append(ingressRules, IngressRule{
					Hostname: host,
					Service:  serviceURL,
				})
				slogf.Infof("mapped %s -> %s", host, serviceURL)
			}
		}
	}

	// Sort for deterministic ordering
	sort.Slice(ingressRules, func(i, j int) bool {
		return ingressRules[i].Hostname < ingressRules[j].Hostname
	})

	// Catch-all 404 rule
	ingressRules = append(ingressRules, IngressRule{
		Service: "http_status:404",
	})

	reqBody := TunnelConfigRequest{
		Config: TunnelConfig{
			Ingress: ingressRules,
		},
	}

	slogf.Debugf("built ingress rules: %v", ingressRules)

	var cfResp map[string]any
	path := fmt.Sprintf("/accounts/%s/cfd_tunnel/%s/configurations", config.CloudFlareAccountID, config.CloudFlareTunnelID)

	if err := client.CloudFlareClient.Put(ctx, path, reqBody, &cfResp); err != nil {
		return fmt.Errorf("error while updating tunnel configuration: %w", err)
	}

	slogf.Infof("pushed %d ingress rules (plus 404 default) to tunnel %s", len(ingressRules)-1, config.CloudFlareTunnelID)
	return nil
}

// chooseServicePort:
// - If svc has SERVICE_UPSTREAM_PORT_LABEL and it parses as a valid port, use it.
// - Else use the first exposed port from spec.ports.
// - If no ports at all, return 0 (caller will skip service and log warning).
func chooseServicePort(svc *corev1.Service, upstreamPortAnnotation string) int32 {
	// 1) Try label
	if raw, ok := svc.Annotations[upstreamPortAnnotation]; ok && strings.TrimSpace(raw) != "" {
		raw = strings.TrimSpace(raw)
		val, err := strconv.Atoi(raw)
		if err != nil || val <= 0 || val > 65535 {
			slogf.Warnf("service %s/%s has invalid %s=%q; falling back to first exposed port",
				svc.Namespace, svc.Name, upstreamPortAnnotation, raw)
		} else {
			return int32(val)
		}
	}

	// 2) Fall back to first exposed port
	if len(svc.Spec.Ports) > 0 {
		return svc.Spec.Ports[0].Port
	}

	// 3) No ports at all → skip
	slogf.Warnf("service %s/%s has no ports; cannot determine upstream port", svc.Namespace, svc.Name)
	return 0
}
