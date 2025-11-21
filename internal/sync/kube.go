package sync

import (
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"tunnel/internal/runtime"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// SyncKube reads Kubernetes services and constructs desired SyncState.
func SyncKube(runtime *runtime.Runtime) (*SyncState, error) {
	runtime.Logger.Info("start reading kube state")
	newState := NewSyncState()

	namespacesList, err := runtime.Client.KubeClient.CoreV1().Namespaces().List(runtime.Ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list namespaces: %w", err)
	}

	var namespaces []string
	for _, ns := range namespacesList.Items {
		namespaces = append(namespaces, ns.Name)
	}
	sort.Strings(namespaces)
	runtime.Logger.Debug("read namespaces", slog.String("namespaces", strings.Join(namespaces, ", ")))

	for _, namespace := range namespaces {
		runtime.Logger.Debug("traversing namespace", slog.String("namespace", namespace))
		svcList, err := runtime.Client.KubeClient.CoreV1().Services(namespace).List(runtime.Ctx, metav1.ListOptions{})
		if err != nil {
			runtime.Logger.Warn("failed to read services in namespace", slog.String("namespace", namespace), slog.String("error", err.Error()))
			continue
		}

		for _, svc := range svcList.Items {
			runtime.Logger.Debug("traversing service", slog.String("namespace", namespace), slog.String("service", svc.Name))
			hostnamesStr, ok := svc.Annotations[runtime.Config.ServiceHostnamesAnnotation]
			if !ok || strings.TrimSpace(hostnamesStr) == "" {
				runtime.Logger.Debug("traversing service: missing hostnames annotation, skipping", slog.String("namespace", namespace), slog.String("service", svc.Name))
				continue
			}

			// Determine upstream port:
			// 1) Check SERVICE_UPSTREAM_PORT_LABEL (default: cloudflare-tunnel-upstream-port)
			// 2) Fall back to first exposed port
			// 3) If none -> skip service with warning
			port := chooseServicePort(runtime, &svc)
			if port == 0 {
				runtime.Logger.Info("service has no usable port; skipping", slog.String("namespace", namespace), slog.String("service", svc.Name))
				continue
			}

			serviceFQDN := fmt.Sprintf("%s.%s.svc.cluster.local", svc.Name, namespace)
			serviceURL := fmt.Sprintf("http://%s:%d", serviceFQDN, port)

			// Domains may be comma- and/or space-separated.
			raw := strings.ReplaceAll(hostnamesStr, ",", " ")
			rawDomains := strings.Fields(raw)
			for _, d := range rawDomains {
				hostname := strings.TrimSpace(d)
				if hostname == "" {
					continue
				}

				runtime.Logger.Info("mapping hostname to service", slog.String("namespace", namespace), slog.String("service", svc.Name), slog.String("hostname", hostname), slog.String("serviceURL", serviceURL))
				err := newState.Append(hostname, serviceURL)
				if err != nil {
					runtime.Logger.Warn("failed to map hostname to service; skipping", slog.String("hostname", hostname), slog.String("service", serviceURL), slog.String("error", err.Error()))
					continue
				}
			}
		}
	}
	runtime.Logger.Info("stop reading kube state", slog.Int("len", len(newState.HostToService)))
	return newState, nil
}

// chooseServicePort:
// - If svc has SERVICE_UPSTREAM_PORT_LABEL and it parses as a valid port, use it.
// - Else use the first exposed port from spec.ports.
// - If no ports at all, return 0 (caller will skip service and log warning).
func chooseServicePort(runtime *runtime.Runtime, svc *corev1.Service) int32 {
	// 1) Try label
	if raw, ok := svc.Annotations[runtime.Config.ServiceUpstreamPortAnnotation]; ok && strings.TrimSpace(raw) != "" {
		raw = strings.TrimSpace(raw)
		val, err := strconv.Atoi(raw)
		if err != nil || val <= 0 || val > 65535 {
			runtime.Logger.Warn("service has invalid port annotation; falling back to first exposed port",
				slog.String("namespace", svc.Namespace),
				slog.String("service", svc.Name),
				slog.String("annotation", runtime.Config.ServiceUpstreamPortAnnotation),
				slog.String("invalidValue", raw),
			)
		}

		runtime.Logger.Debug("service has port annotation; using it as upstream port",
			slog.String("namespace", svc.Namespace),
			slog.String("service", svc.Name),
			slog.String("annotation", runtime.Config.ServiceUpstreamPortAnnotation),
			slog.Int("value", int(val)),
		)
		return int32(val)
	}

	// 2) Fall back to first exposed port
	if len(svc.Spec.Ports) > 0 {
		port := svc.Spec.Ports[0].Port
		runtime.Logger.Debug("service has no port annotation; falling back to first exposed port",
			slog.String("namespace", svc.Namespace),
			slog.String("service", svc.Name),
			slog.String("annotation", runtime.Config.ServiceUpstreamPortAnnotation),
			slog.Int("value", int(port)),
		)
		return port
	}

	// 3) No ports at all → skip
	runtime.Logger.Warn("service has no ports; cannot determine upstream port",
		slog.String("namespace", svc.Namespace),
		slog.String("service", svc.Name),
		slog.String("annotation", runtime.Config.ServiceUpstreamPortAnnotation),
	)

	return 0
}
