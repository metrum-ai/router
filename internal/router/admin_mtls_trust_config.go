// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

// AdminMTLSTrustConfig is the owned mTLS configuration. The listener's outer
// authentication block will consume it when its bootstrap dependency lands.
// Flat fields describe one explicitly named domain; never mix with TrustDomains.
type AdminMTLSTrustConfig struct {
	Issuer         string                       `yaml:"issuer" json:"issuer,omitempty"`
	CAFiles        []string                     `yaml:"ca_files" json:"caFiles,omitempty"`
	Subjects       []AdminMTLSSubjectConfig     `yaml:"subjects" json:"subjects,omitempty"`
	TrustDomains   []AdminMTLSTrustDomainConfig `yaml:"trust_domains" json:"trustDomains,omitempty"`
	ReloadInterval string                       `yaml:"reload_interval" json:"reloadInterval,omitempty"`
}
type AdminMTLSTrustDomainConfig struct {
	Issuer   string                   `yaml:"issuer" json:"issuer"`
	CAFiles  []string                 `yaml:"ca_files" json:"caFiles"`
	Subjects []AdminMTLSSubjectConfig `yaml:"subjects" json:"subjects,omitempty"`
}
type AdminMTLSSubjectConfig struct {
	SAN     string `yaml:"san" json:"san"`
	Subject string `yaml:"subject" json:"subject"`
}
