package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

const (
	maxQuerySize        = 100000
	maxGraphQLVarLength = 10000
	maxGraphQLVarNum    = 1000000
	maxGitHubNameLength = 100
	graphQLEndpoint     = "https://api.github.com/graphql"
)

// MakeGraphQLRequest makes a GraphQL request to GitHub API.
func (c *Client) MakeGraphQLRequest(ctx context.Context, query string, variables map[string]any) (map[string]any, error) {
	if err := validateGraphQLVariables(variables); err != nil {
		return nil, fmt.Errorf("invalid GraphQL variables: %w", err)
	}

	queryType := extractGraphQLQueryType(query)
	querySize := len(query)

	if querySize > maxQuerySize {
		return nil, fmt.Errorf("GraphQL query too large: %d chars (max %d)", querySize, maxQuerySize)
	}

	slog.InfoContext(ctx, "Executing GraphQL query", "type", queryType, "size", querySize)
	if len(variables) > 0 {
		slog.DebugContext(ctx, "GraphQL query variables", "type", queryType, "count", len(variables))
	}

	payload := map[string]any{
		"query":     query,
		"variables": variables,
	}

	bodyBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal GraphQL request: %w", err)
	}

	start := time.Now()

	var result map[string]any
	err = retryWithBackoff(ctx, fmt.Sprintf("GraphQL %s query", queryType), func() error {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, graphQLEndpoint, bytes.NewReader(bodyBytes))
		if err != nil {
			return fmt.Errorf("failed to create GraphQL request: %w", err)
		}

		authToken := c.token
		if c.isAppAuth && c.currentOrg != "" {
			installToken, err := c.getInstallationToken(ctx, c.currentOrg)
			if err == nil {
				authToken = installToken
				slog.DebugContext(ctx, "Using installation token for GraphQL", "org", c.currentOrg)
			} else {
				slog.WarnContext(ctx, "Failed to get installation token for GraphQL, using JWT", "org", c.currentOrg, "error", err)
			}
		}

		req.Header.Set("Authorization", "Bearer "+authToken)
		req.Header.Set("Content-Type", "application/json")

		resp, err := c.httpClient.Do(req)
		if err != nil {
			return fmt.Errorf("graphql request failed: %w", err)
		}
		defer func() {
			if err := resp.Body.Close(); err != nil {
				slog.WarnContext(ctx, "Failed to close response body", "error", err)
			}
		}()

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return fmt.Errorf("failed to read response: %w", err)
		}

		if resp.StatusCode != http.StatusOK {
			slog.ErrorContext(ctx, "GraphQL query failed", "type", queryType, "status", resp.StatusCode, "org", c.currentOrg, "body", string(body))
			return fmt.Errorf("graphql request failed with status %d: %s", resp.StatusCode, string(body))
		}

		if err := json.Unmarshal(body, &result); err != nil {
			return fmt.Errorf("failed to decode GraphQL response: %w", err)
		}

		if errors, ok := result["errors"]; ok {
			slog.ErrorContext(ctx, "GraphQL query returned errors", "type", queryType, "org", c.currentOrg, "errors", errors)
			return fmt.Errorf("graphql errors: %v", errors)
		}

		return nil
	})
	if err != nil {
		return nil, err
	}

	duration := time.Since(start)
	slog.InfoContext(ctx, "GraphQL query completed", "type", queryType, "duration", duration)
	return result, nil
}

// validateGraphQLVariables validates GraphQL variables to prevent injection.
func validateGraphQLVariables(variables map[string]any) error {
	for key, value := range variables {
		if strings.ContainsAny(key, "{}[]\"'\n\r\t") {
			return fmt.Errorf("invalid character in variable key: %s", key)
		}

		if str, ok := value.(string); ok {
			if strings.Contains(str, "__schema") || strings.Contains(str, "__type") {
				return errors.New("introspection queries not allowed in variables")
			}
			if len(str) > maxGraphQLVarLength {
				return fmt.Errorf("variable value too long: %d chars", len(str))
			}
			if key == "owner" || key == "repo" || key == "org" || key == "login" {
				if strings.ContainsAny(str, "../\\\n\r\x00") || len(str) > maxGitHubNameLength || str == "" {
					return fmt.Errorf("invalid GitHub name in variable %s: %s", key, str)
				}
			}
		}

		if num, ok := value.(int); ok {
			if num < 0 || num > maxGraphQLVarNum {
				return fmt.Errorf("numeric variable out of range: %d", num)
			}
		}
	}
	return nil
}

// extractGraphQLQueryType extracts a descriptive query type from GraphQL query for debugging.
func extractGraphQLQueryType(query string) string {
	query = strings.TrimSpace(query)
	lines := strings.Split(query, "\n")

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "query(") {
			for i := 1; i < len(lines); i++ {
				fieldLine := strings.TrimSpace(lines[i])
				if fieldLine == "" || strings.HasPrefix(fieldLine, "}") {
					continue
				}

				if strings.Contains(fieldLine, "organization(") {
					return "organization-repositories"
				}

				if strings.Contains(fieldLine, "user(") {
					return "user-repositories"
				}

				if strings.Contains(fieldLine, "repository(") {
					if !strings.Contains(query, "pullRequests") {
						return "repository-query"
					}
					if strings.Contains(query, "history") {
						return "repository-commit-history"
					}
					return "repository-pullrequests"
				}

				if idx := strings.Index(fieldLine, "("); idx != -1 {
					return strings.TrimSpace(fieldLine[:idx])
				}
				if idx := strings.Index(fieldLine, " "); idx != -1 {
					return strings.TrimSpace(fieldLine[:idx])
				}
				return fieldLine
			}
			return "unknown-query"
		}
	}

	if strings.Contains(query, "organization") && strings.Contains(query, "repositories") {
		return "org-batch-prs"
	}
	if strings.Contains(query, "repository") && strings.Contains(query, "pullRequests") {
		return "repo-recent-prs"
	}
	if strings.Contains(query, "history") {
		return "commit-history"
	}

	return "unknown-graphql"
}
