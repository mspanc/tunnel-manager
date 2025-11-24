package sync

import (
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"tunnel/internal/runtime"

	"github.com/cloudflare/cloudflare-go/v6"
	"github.com/cloudflare/cloudflare-go/v6/option"
)

const managedCommentMarker = "managed by tunnel-manager"

// zoneSummary is a minimal representation of a Cloudflare zone.
type zoneSummary struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type zoneListResponse struct {
	Result     []zoneSummary `json:"result"`
	ResultInfo struct {
		Page       int `json:"page"`
		TotalPages int `json:"total_pages"`
	} `json:"result_info"`
}

// dnsRecord is a minimal representation of a Cloudflare DNS record.
type dnsRecord struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Name    string `json:"name"`
	Content string `json:"content"`
	Comment string `json:"comment"`
}

type dnsRecordsListResponse struct {
	Result     []dnsRecord `json:"result"`
	ResultInfo struct {
		Page       int `json:"page"`
		TotalPages int `json:"total_pages"`
	} `json:"result_info"`
}

// SyncDNS synchronizes DNS in Cloudflare for given SyncState and
// Cloudflare tunnel configuration from rt.Config.
//
// It will:
//   - read all A, AAAA and CNAME records
//   - manage only CNAMEs that contain "xxx" in the comment
//   - if there are A/AAAA records for a hostname, it will NOT create a CNAME
//     (to avoid conflicts)
//   - delete managed CNAMEs for hostnames no longer present in SyncState
//   - create/update managed CNAMEs to point to "<TunnelID>.cfargotunnel.com"
func SyncDNS(rt *runtime.Runtime, state *SyncState) error {
	logger := rt.Logger
	if logger == nil {
		logger = slog.Default()
	}

	if rt.Client == nil || rt.Client.CloudFlareClient == nil {
		return fmt.Errorf("cloudflare client is nil")
	}
	cf := rt.Client.CloudFlareClient

	if rt.Config == nil {
		return fmt.Errorf("config is nil")
	}

	accountID := rt.Config.CloudFlareAccountID
	tunnelID := rt.Config.CloudFlareTunnelID

	if state.HostToService == nil || state.Len() == 0 {
		logger.Info("no hostnames in SyncState; nothing to sync")
		return nil
	}

	target := tunnelID + ".cfargotunnel.com"

	logger.Info("starting Cloudflare DNS sync",
		"account_id", accountID,
		"tunnel_id", tunnelID,
		"target", target,
		"hosts_count", len(state.HostToService),
	)

	// 1) Load all zones in the account.
	zones, err := loadZones(rt, accountID)
	if err != nil {
		return fmt.Errorf("loading zones: %w", err)
	}
	if len(zones) == 0 {
		logger.Warn("no zones found for account, nothing to sync", "account_id", accountID)
		return nil
	}

	zoneIDByName := make(map[string]string, len(zones))
	for _, z := range zones {
		zoneIDByName[z.Name] = z.ID
	}

	// 2) Distribute hostnames across zones using best suffix match.
	zoneHosts := make(map[string][]string) // zoneName -> []hostname
	for host := range state.HostToService {
		hostNorm := normalizeHost(host)
		if hostNorm == "" {
			continue
		}
		zoneName := bestMatchingZone(hostNorm, zones)
		if zoneName == "" {
			logger.Warn("no matching zone found for hostname; skipping",
				"hostname", hostNorm,
				"account_id", accountID,
			)
			continue
		}
		zoneHosts[zoneName] = append(zoneHosts[zoneName], hostNorm)
	}

	// 3) For each zone, sync A/AAAA/CNAME records according to state.
	for zoneName, hosts := range zoneHosts {
		zoneID := zoneIDByName[zoneName]
		if zoneID == "" {
			logger.Error("zone id not found for zone name; skipping zone",
				"zone_name", zoneName,
				"account_id", accountID,
			)
			continue
		}

		if err := syncZoneRecords(rt, cf, zoneID, zoneName, hosts, state, target); err != nil {
			return fmt.Errorf("sync zone %s (%s): %w", zoneName, zoneID, err)
		}
	}

	logger.Info("Cloudflare DNS sync finished successfully",
		"zones", len(zoneHosts),
	)
	return nil
}

// loadZones loads all zones for a given account ID using the generic client.Get.
func loadZones(rt *runtime.Runtime, accountID string) ([]zoneSummary, error) {
	logger := rt.Logger
	if logger == nil {
		logger = slog.Default()
	}
	client := rt.Client.CloudFlareClient

	var zones []zoneSummary
	page := 1

	for {
		var resp zoneListResponse

		logger.Debug("requesting zones page",
			"page", page,
			"account_id", accountID,
		)

		err := client.Get(
			rt.Ctx,
			"/zones",
			nil,
			&resp,
			option.WithQuery("account.id", accountID),
			option.WithQuery("page", fmt.Sprintf("%d", page)),
			option.WithQuery("per_page", "100"),
			option.WithQuery("status", "active"),
		)
		if err != nil {
			return nil, fmt.Errorf("GET /zones page %d: %w", page, err)
		}

		zones = append(zones, resp.Result...)

		if resp.ResultInfo.Page >= resp.ResultInfo.TotalPages || resp.ResultInfo.TotalPages == 0 {
			break
		}
		page++
	}

	return zones, nil
}

// syncZoneRecords synchronizes A/AAAA/CNAME records for a single zone.
func syncZoneRecords(
	rt *runtime.Runtime,
	client *cloudflare.Client,
	zoneID, zoneName string,
	hosts []string,
	state *SyncState,
	target string,
) error {
	logger := rt.Logger
	if logger == nil {
		logger = slog.Default()
	}

	logger.Info("syncing zone DNS",
		"zone_id", zoneID,
		"zone_name", zoneName,
		"hosts_count", len(hosts),
	)

	hostSet := make(map[string]struct{}, len(hosts))
	for _, h := range hosts {
		hostSet[h] = struct{}{}
	}

	// Load all records (we'll filter types in code).
	records, err := loadDNSRecords(rt, client, zoneID)
	if err != nil {
		return fmt.Errorf("loading DNS records: %w", err)
	}

	// Index CNAMEs and detect A/AAAA conflicts.
	cnameByName := make(map[string]dnsRecord)
	hasAorAAAA := make(map[string]bool)

	for _, rec := range records {
		name := normalizeHost(rec.Name)
		switch rec.Type {
		case "A", "AAAA":
			hasAorAAAA[name] = true
		case "CNAME":
			// only care about CNAMEs for sync logic
			cnameByName[name] = rec
		}
	}

	seen := make(map[string]bool, len(hosts))

	// Handle existing CNAMEs according to rules.
	for name, rec := range cnameByName {
		_, shouldBeManaged := hostSet[name]
		isManaged := strings.Contains(rec.Comment, managedCommentMarker)

		switch {
		// 1) CNAME for hostname NOT in SyncState & managed -> delete.
		case !shouldBeManaged && isManaged:
			logger.Info("deleting managed CNAME for hostname not present in SyncState",
				"zone_id", zoneID,
				"zone_name", zoneName,
				"hostname", name,
				"record_id", rec.ID,
				"content", rec.Content,
			)
			if err := deleteDNSRecord(rt, client, zoneID, rec.ID); err != nil {
				return fmt.Errorf("delete CNAME record %s (%s): %w", rec.ID, name, err)
			}

		// 2) CNAME for hostname NOT in SyncState & NOT managed -> leave, log warning.
		case !shouldBeManaged && !isManaged:
			logger.Warn("unmanaged CNAME for hostname not present in SyncState; leaving untouched",
				"zone_id", zoneID,
				"zone_name", zoneName,
				"hostname", name,
				"record_id", rec.ID,
				"content", rec.Content,
			)

		// 3) CNAME for hostname present in SyncState & managed; if target diff -> update.
		case shouldBeManaged && isManaged:
			seen[name] = true

			if !equalDNSHost(rec.Content, target) {
				logger.Info("updating managed CNAME to tunnel target",
					"zone_id", zoneID,
					"zone_name", zoneName,
					"hostname", name,
					"record_id", rec.ID,
					"old_content", rec.Content,
					"new_content", target,
				)
				if err := updateCNAMERecordTarget(rt, client, zoneID, rec.ID, target); err != nil {
					return fmt.Errorf("update CNAME record %s (%s): %w", rec.ID, name, err)
				}
			} else {
				logger.Debug("managed CNAME already pointing to tunnel; no change",
					"zone_id", zoneID,
					"zone_name", zoneName,
					"hostname", name,
					"record_id", rec.ID,
				)
			}

		// 4) CNAME for hostname present in SyncState but NOT managed -> warn, do not touch.
		case shouldBeManaged && !isManaged:
			seen[name] = true
			logger.Warn("hostname present in SyncState but CNAME is not managed (no marker in comment); leaving untouched",
				"zone_id", zoneID,
				"zone_name", zoneName,
				"hostname", name,
				"record_id", rec.ID,
				"content", rec.Content,
				"comment", rec.Comment,
			)
		}
	}

	// Create missing CNAMEs, but skip if there are A/AAAA records.
	for _, host := range hosts {
		if seen[host] {
			continue
		}

		if hasAorAAAA[host] {
			logger.Warn("A/AAAA records exist for hostname; skipping CNAME creation to avoid conflict",
				"zone_id", zoneID,
				"zone_name", zoneName,
				"hostname", host,
			)
			continue
		}

		service := state.HostToService[host]

		logger.Info("creating managed CNAME for hostname",
			"zone_id", zoneID,
			"zone_name", zoneName,
			"hostname", host,
			"target", target,
			"service", service,
		)

		if err := createCNAMERecord(rt, client, zoneID, host, target); err != nil {
			return fmt.Errorf("create CNAME for host %s: %w", host, err)
		}
	}

	return nil
}

// loadDNSRecords loads all DNS records for given zone ID and filters to the
// types we're interested in (A, AAAA, CNAME).
func loadDNSRecords(
	rt *runtime.Runtime,
	client *cloudflare.Client,
	zoneID string,
) ([]dnsRecord, error) {
	logger := rt.Logger
	if logger == nil {
		logger = slog.Default()
	}

	var records []dnsRecord
	page := 1

	for {
		var resp dnsRecordsListResponse

		logger.Debug("requesting DNS records page",
			"zone_id", zoneID,
			"page", page,
		)

		err := client.Get(
			rt.Ctx,
			fmt.Sprintf("/zones/%s/dns_records", url.PathEscape(zoneID)),
			nil,
			&resp,
			option.WithQuery("page", fmt.Sprintf("%d", page)),
			option.WithQuery("per_page", "100"),
		)
		if err != nil {
			return nil, fmt.Errorf("GET /zones/%s/dns_records page %d: %w", zoneID, page, err)
		}

		for _, r := range resp.Result {
			switch r.Type {
			case "A", "AAAA", "CNAME":
				records = append(records, r)
			default:
				// ignore other record types
			}
		}

		if resp.ResultInfo.Page >= resp.ResultInfo.TotalPages || resp.ResultInfo.TotalPages == 0 {
			break
		}
		page++
	}

	return records, nil
}

// deleteDNSRecord deletes a DNS record by ID.
func deleteDNSRecord(
	rt *runtime.Runtime,
	client *cloudflare.Client,
	zoneID, recordID string,
) error {
	var res struct{}
	err := client.Delete(
		rt.Ctx,
		fmt.Sprintf("/zones/%s/dns_records/%s", url.PathEscape(zoneID), url.PathEscape(recordID)),
		nil,
		&res,
	)
	if err != nil {
		return fmt.Errorf("DELETE /zones/%s/dns_records/%s: %w", zoneID, recordID, err)
	}
	return nil
}

// createCNAMERecord creates a new managed CNAME.
func createCNAMERecord(
	rt *runtime.Runtime,
	client *cloudflare.Client,
	zoneID, hostname, target string,
) error {
	body := map[string]any{
		"type":    "CNAME",
		"name":    hostname,
		"content": target,
		"ttl":     1, // "auto"
		"proxied": true,
		"comment": managedCommentMarker,
	}

	var resp struct {
		Success bool `json:"success"`
	}
	err := client.Post(
		rt.Ctx,
		fmt.Sprintf("/zones/%s/dns_records", url.PathEscape(zoneID)),
		body,
		&resp,
	)
	if err != nil {
		return fmt.Errorf("POST /zones/%s/dns_records: %w", zoneID, err)
	}
	if !resp.Success {
		return fmt.Errorf("Cloudflare API reported failure creating CNAME")
	}
	return nil
}

// updateCNAMERecordTarget updates the content + comment of an existing CNAME.
func updateCNAMERecordTarget(
	rt *runtime.Runtime,
	client *cloudflare.Client,
	zoneID, recordID, target string,
) error {
	body := map[string]any{
		"content": target,
		"comment": managedCommentMarker,
	}

	var resp struct {
		Success bool `json:"success"`
	}
	err := client.Patch(
		rt.Ctx,
		fmt.Sprintf("/zones/%s/dns_records/%s", url.PathEscape(zoneID), url.PathEscape(recordID)),
		body,
		&resp,
	)
	if err != nil {
		return fmt.Errorf("PATCH /zones/%s/dns_records/%s: %w", zoneID, recordID, err)
	}
	if !resp.Success {
		return fmt.Errorf("Cloudflare API reported failure updating CNAME")
	}
	return nil
}

// bestMatchingZone chooses the zone whose name is the longest suffix of hostname.
func bestMatchingZone(hostname string, zones []zoneSummary) string {
	hostname = normalizeHost(hostname)
	best := ""
	for _, z := range zones {
		name := normalizeHost(z.Name)
		if hostname == name || strings.HasSuffix(hostname, "."+name) {
			if len(name) > len(best) {
				best = name
			}
		}
	}
	return best
}

// equalDNSHost compares DNS hostnames ignoring trailing dot & case.
func equalDNSHost(a, b string) bool {
	return normalizeHost(a) == normalizeHost(b)
}

func normalizeHost(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimSuffix(s, ".")
	return strings.ToLower(s)
}
