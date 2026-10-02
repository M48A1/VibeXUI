package server

import (
	"math"
	"time"
	"vibexui/internal/model"
)

func addBytes(a, b uint64) uint64 {
	if math.MaxUint64-a < b {
		return math.MaxUint64
	}
	return a + b
}

// Reports are cumulative within one persisted Agent epoch. High-water marks
// make repeated and delayed reports harmless, while a new epoch permits resets.
func accountTraffic(st *model.State, s *model.Server, r model.Report) {
	if r.StatsEpoch == "" {
		return
	}
	if s.ClientTraffic == nil {
		s.ClientTraffic = map[string]model.Traffic{}
	}
	if s.StatsEpoch != r.StatsEpoch {
		for id := range s.ClientTraffic {
			s.ClientTraffic[id] = model.Traffic{}
		}
		s.NodeTraffic = map[string]model.Traffic{}
		s.StatsEpoch = r.StatsEpoch
	}
	now := time.Now()
	sampleAt := r.StatsCollectedAt
	if sampleAt.IsZero() || sampleAt.After(now) {
		sampleAt = now
	}
	if r.StatsError == "" && r.Running {
		s.StatsUpdatedAt = sampleAt
	}
	if s.NodeTraffic == nil {
		s.NodeTraffic = map[string]model.Traffic{}
	}
	validNodes := map[string]bool{}
	for i := range st.Nodes {
		n := &st.Nodes[i]
		if n.ServerID != s.ID {
			continue
		}
		validNodes[n.ID] = true
		current, reported := r.NodeTraffic[n.ID]
		if !reported {
			continue
		}
		previous := s.NodeTraffic[n.ID]
		if current.Upload > previous.Upload {
			n.Upload = addBytes(n.Upload, current.Upload-previous.Upload)
			previous.Upload = current.Upload
		}
		if current.Download > previous.Download {
			n.Download = addBytes(n.Download, current.Download-previous.Download)
			previous.Download = current.Download
		}
		s.NodeTraffic[n.ID] = previous
		if r.StatsError == "" && r.Running {
			n.TrafficUpdatedAt = sampleAt
		}
	}
	for id := range s.NodeTraffic {
		if !validNodes[id] {
			delete(s.NodeTraffic, id)
		}
	}
	existing := map[string]bool{}
	for i := range st.Clients {
		c := &st.Clients[i]
		existing[c.ID] = true
		previous, known := s.ClientTraffic[c.ID]
		assigned := false
		for _, id := range c.NodeIDs {
			if validNodes[id] {
				assigned = true
				break
			}
		}
		if !known && !assigned {
			continue
		}
		current, reported := r.ClientTraffic[c.ID]
		if !reported {
			continue
		}
		if c.ServerTraffic == nil {
			c.ServerTraffic = map[string]model.Traffic{}
		}
		perServer := c.ServerTraffic[s.ID]
		if c.Enabled && c.FirstUseDays > 0 && c.FirstUsedAt.IsZero() && (current.Upload > previous.Upload || current.Download > previous.Download) {
			c.FirstUsedAt = now
			c.ExpiresAt = now.Add(time.Duration(c.FirstUseDays) * 24 * time.Hour)
		}
		if current.Upload > previous.Upload {
			delta := current.Upload - previous.Upload
			c.Upload = addBytes(c.Upload, delta)
			perServer.Upload = addBytes(perServer.Upload, delta)
			previous.Upload = current.Upload
		}
		if current.Download > previous.Download {
			delta := current.Download - previous.Download
			c.Download = addBytes(c.Download, delta)
			perServer.Download = addBytes(perServer.Download, delta)
			previous.Download = current.Download
		}
		s.ClientTraffic[c.ID] = previous
		c.ServerTraffic[s.ID] = perServer
		if r.StatsError == "" && r.Running {
			c.TrafficUpdatedAt = sampleAt
		}
	}
	for id := range s.ClientTraffic {
		if !existing[id] {
			delete(s.ClientTraffic, id)
		}
	}
}
