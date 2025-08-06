package main

import (
	"fmt"
	"strings"
)

// outputFormatter provides modern, minimal output formatting.
type outputFormatter struct {
	verbose bool
}

// formatPRHeader formats the PR information header.
func (*outputFormatter) formatPRHeader(pr *PullRequest) string {
	var b strings.Builder

	b.WriteString("\n")
	b.WriteString("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
	b.WriteString(fmt.Sprintf("  PR #%d: %s\n", pr.Number, pr.Title))
	b.WriteString(fmt.Sprintf("  Author: @%s | State: %s", pr.Author, pr.State))
	if pr.Draft {
		b.WriteString(" [DRAFT]")
	}
	b.WriteString("\n")
	b.WriteString("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")

	return b.String()
}

// formatCandidates formats the reviewer candidates with ranking.
func (of *outputFormatter) formatCandidates(candidates []ReviewerCandidate, existing []string) string {
	if len(candidates) == 0 {
		return "\n  ⚠️  No suitable reviewers found\n"
	}

	var b strings.Builder
	b.WriteString("\n  🎯 Reviewer Recommendations:\n\n")

	// Format each candidate
	for i, candidate := range candidates {
		rank := i + 1
		icon := of.methodIcon(candidate.SelectionMethod)

		b.WriteString(fmt.Sprintf("  %d. %s @%s\n", rank, icon, candidate.Username))
		b.WriteString(fmt.Sprintf("     └─ %s (score: %d)\n",
			of.formatMethod(candidate.SelectionMethod),
			candidate.ContextScore))

		if i < len(candidates)-1 {
			b.WriteString("\n")
		}
	}

	// Show existing reviewers if any
	if len(existing) > 0 {
		b.WriteString("\n  📋 Current Reviewers: ")
		for i, r := range existing {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString("@" + r)
		}
		b.WriteString("\n")
	}

	return b.String()
}

// formatAction formats the action taken.
func (*outputFormatter) formatAction(action string, pr *PullRequest, reviewers []string) string {
	var b strings.Builder

	switch action {
	case "assigned":
		b.WriteString(fmt.Sprintf("\n  ✅ Assigned reviewers to PR #%d: ", pr.Number))
		for i, r := range reviewers {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString("@" + r)
		}
	case "dry-run":
		b.WriteString(fmt.Sprintf("\n  🔍 [DRY RUN] Would assign to PR #%d: ", pr.Number))
		for i, r := range reviewers {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString("@" + r)
		}
	case "skipped":
		b.WriteString(fmt.Sprintf("\n  ⏭️  Skipped PR #%d (already has reviewers)", pr.Number))
	default:
		b.WriteString(fmt.Sprintf("\n  ❓ Unknown action for PR #%d", pr.Number))
	}

	b.WriteString("\n")
	return b.String()
}

// formatSummary formats the final summary.
func (*outputFormatter) formatSummary(processed, assigned, skipped int) string {
	var b strings.Builder

	b.WriteString("\n")
	b.WriteString("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
	b.WriteString("  📊 Summary\n")
	b.WriteString("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
	b.WriteString(fmt.Sprintf("  • Processed: %d PRs\n", processed))
	b.WriteString(fmt.Sprintf("  • Assigned:  %d PRs\n", assigned))
	b.WriteString(fmt.Sprintf("  • Skipped:   %d PRs\n", skipped))
	b.WriteString("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")

	return b.String()
}

// methodIcon returns an icon for the selection method.
func (*outputFormatter) methodIcon(method string) string {
	switch method {
	case selectionAssignee:
		return "👤"
	case selectionAuthorOverlap:
		return "🔍"
	case selectionAuthorDirectory:
		return "📁"
	case selectionAuthorProject:
		return "🏗️"
	case selectionReviewerCommenter:
		return "💬"
	case selectionReviewerOverlap:
		return "🔎"
	case selectionReviewerDirectory:
		return "📂"
	case selectionReviewerProject:
		return "🏛️"
	default:
		return "•"
	}
}

// formatMethod formats the selection method description.
func (*outputFormatter) formatMethod(method string) string {
	switch method {
	case selectionAssignee:
		return "PR assignee (highest priority)"
	case selectionAuthorOverlap:
		return "Modified same lines recently"
	case selectionAuthorDirectory:
		return "Active in this directory"
	case selectionAuthorProject:
		return "Active in this project"
	case selectionReviewerCommenter:
		return "Recent PR commenter"
	case selectionReviewerOverlap:
		return "Reviewed similar changes"
	case selectionReviewerDirectory:
		return "Reviews in this directory"
	case selectionReviewerProject:
		return "Active reviewer in project"
	default:
		return method
	}
}
