// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

func (s *Service) audioCapabilityMismatch(rc *requestContext, targets []Target, req *IRRequest, dialect string) (string, string) {
	// Diagnose audio only when removing that output requirement makes a
	// non-audio target eligible. Otherwise report the unrelated capability or
	// endpoint failure rather than blaming an audio-capable target.
	if requestWantsChatAudioOutput(req, dialect) {
		withoutAudio := *req
		withoutAudio.Raw = make(map[string]any, len(req.Raw))
		for k, v := range req.Raw {
			if k != "audio" && k != "modalities" {
				withoutAudio.Raw[k] = v
			}
		}
		// Retain the actual request-size estimate used for selection. Recomputing
		// it without the request context would incorrectly treat its size as zero.
		if candidates := s.targetsForRequest(rc, targets, &withoutAudio, dialect); len(candidates) > 0 {
			audioEligible := false
			for _, target := range candidates {
				out := targetDialect(s.cfg.Provider[target.Provider], target)
				if targetSupportsChatAudioOutput(target, req, dialect, out) {
					audioEligible = true
					break
				}
			}
			if !audioEligible {
				return "unsupported_capability", "modalities"
			}
		}
	}
	return s.capabilityMismatch(targets, req, dialect)
}
