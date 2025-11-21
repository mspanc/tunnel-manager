package sync

import (
	"fmt"
	"log/slog"
)

// SyncState represents desired DNS/tunnel state: hostname -> service.
// Service is carried mostly for logging/context purposes in this module.
type SyncState struct {
	HostToService map[string]string
}

func NewSyncState() *SyncState {
	return &SyncState{
		HostToService: make(map[string]string),
	}
}

func (s *SyncState) Len() int {
	return len(s.HostToService)
}

func (s *SyncState) Append(hostname, service string) error {
	if _, exists := s.HostToService[hostname]; exists {
		return fmt.Errorf("hostname %q is already mapped to service %q", hostname, s.HostToService[hostname])
	}
	s.HostToService[hostname] = service
	return nil
}

func (s *SyncState) Print(logger *slog.Logger) {
	for host, service := range s.HostToService {
		logger.Info("hostname -> service", slog.String("hostname", host), slog.String("service", service))
	}
}
