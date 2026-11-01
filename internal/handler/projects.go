package handler

import (
	"context"
	"math"

	"github.com/base48/member-portal/internal/db"
)

// formatCrowns formats an amount in crowns the Czech way: "44 000" when it is
// whole, "22 977,90" when it is not.
func formatCrowns(v float64) string {
	cents := int64(math.Round(v * 100))
	if cents%100 == 0 {
		return formatCentsAsWholeCZK(cents)
	}
	return formatCentsAsCZK(cents)
}

// projectSummary is a project with its symbols and what it raised and spent.
// Admin and member pages build their views from it.
type projectSummary struct {
	db.Project
	VSList []VSInfo
	Raised float64 // incoming payments
	Spent  float64 // outgoing payments, as a positive number
}

// Remaining is what is left of the money raised. Negative when the project
// spent more than it raised and the association covered the difference.
func (p projectSummary) Remaining() float64 {
	return p.Raised - p.Spent
}

// Covered is what the association paid beyond what the project raised.
func (p projectSummary) Covered() float64 {
	return max(p.Spent-p.Raised, 0)
}

// listProjectSummaries loads every project, open ones first, newest first,
// with three queries in total however many projects there are.
func (h *Handler) listProjectSummaries(ctx context.Context) ([]projectSummary, error) {
	projects, err := h.queries.ListProjects(ctx)
	if err != nil {
		return nil, err
	}
	totals, err := h.queries.ListProjectTotals(ctx)
	if err != nil {
		return nil, err
	}
	symbols, err := h.queries.ListAllProjectVS(ctx)
	if err != nil {
		return nil, err
	}

	byProject := make(map[int64]db.ListProjectTotalsRow, len(totals))
	for _, t := range totals {
		byProject[t.ProjectID] = t
	}
	vsByProject := make(map[int64][]VSInfo)
	for _, vs := range symbols {
		vsByProject[vs.ProjectID] = append(vsByProject[vs.ProjectID], VSInfo{VS: vs.Vs, Note: vs.Note.String})
	}

	out := make([]projectSummary, len(projects))
	for i, p := range projects {
		vsList := vsByProject[p.ID]
		if vsList == nil {
			vsList = []VSInfo{}
		}
		out[i] = projectSummary{
			Project: p,
			VSList:  vsList,
			Raised:  byProject[p.ID].Raised,
			Spent:   byProject[p.ID].Spent,
		}
	}
	return out, nil
}
