// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0
package router

import (
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"net/http"
)

type policySetSummary struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Version   int    `json:"version"`
	Status    string `json:"status"`
	CreatedBy string `json:"created_by"`
	CreatedAt string `json:"created_at"`
}
type policySetListDocument struct {
	Items    []policySetSummary `json:"items"`
	Revision int64              `json:"revision"`
}

func (h *policyAdminHandler) listSets(w http.ResponseWriter, r *http.Request) {
	if h.store == nil || h.store.db == nil {
		policyHTTPProblem(w, r, ErrAuthorizationUnavailable)
		return
	}
	page, _ := AdminPageFrom(r.Context())
	limit := page.LimitOr(100)
	values := page.CursorValues()
	descending := page.Descending()
	if len(values) > 1 {
		writeAdminProblemCode(w, r, 400, "invalid-cursor", "")
		return
	}
	var revision policyMutationRevision
	var rows []authzPolicySetRecord
	err := h.store.db.WithContext(r.Context()).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).First(&revision, 1).Error; err != nil {
			return ErrAuthorizationUnavailable
		}
		query := tx.Model(&authzPolicySetRecord{})
		if len(values) > 0 {
			if descending {
				query = query.Where("id < ?", values[0])
			} else {
				query = query.Where("id > ?", values[0])
			}
		}
		if descending {
			query = query.Order("id DESC")
		} else {
			query = query.Order("id ASC")
		}
		return query.Limit(limit + 1).Find(&rows).Error
	})
	if err != nil {
		policyHTTPProblem(w, r, ErrAuthorizationUnavailable)
		return
	}
	if len(rows) > limit {
		rows = rows[:limit]
		if page != nil {
			page.PublishNext(rows[len(rows)-1].ID)
		}
	}
	items := make([]policySetSummary, 0, len(rows))
	for _, row := range rows {
		items = append(items, policySetSummary{row.ID, row.Name, row.Version, row.Status, row.CreatedBy, row.CreatedAt})
	}
	policyHTTPJSON(w, 200, revision.Revision, policySetListDocument{items, revision.Revision})
}
