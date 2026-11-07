// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

// AdminMTLSTrustConfig is the owned mTLS configuration, also aliased by the
// listener's authentication block so strict YAML uses the same schema.
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

func adminMTLSTrustConfigured(c AdminMTLSTrustConfig) bool {
	return c.Issuer != "" || len(c.CAFiles) != 0 || len(c.Subjects) != 0 || len(c.TrustDomains) != 0
}

func adminMTLSHasCAFiles(c AdminMTLSTrustConfig) bool {
	if len(c.CAFiles) != 0 {
		return true
	}
	for _, domain := range c.TrustDomains {
		if len(domain.CAFiles) != 0 {
			return true
		}
	}
	return false
}
