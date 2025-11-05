package github

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func TestValidateGraphQLVariables(t *testing.T) {
	tests := []struct {
		name      string
		variables map[string]any
		wantErr   bool
	}{
		{
			name:      "nil variables",
			variables: nil,
			wantErr:   false,
		},
		{
			name:      "empty variables",
			variables: map[string]any{},
			wantErr:   false,
		},
		{
			name: "valid owner",
			variables: map[string]any{
				"owner": "test-owner",
			},
			wantErr: false,
		},
		{
			name: "valid int",
			variables: map[string]any{
				"number": 123,
			},
			wantErr: false,
		},
		{
			name: "invalid character in key",
			variables: map[string]any{
				"key{with}braces": "value",
			},
			wantErr: true,
		},
		{
			name: "introspection attempt in variable",
			variables: map[string]any{
				"query": "__schema",
			},
			wantErr: true,
		},
		{
			name: "too long string value",
			variables: map[string]any{
				"data": strings.Repeat("a", 10001),
			},
			wantErr: true,
		},
		{
			name: "invalid owner with path traversal",
			variables: map[string]any{
				"owner": "../etc/passwd",
			},
			wantErr: true,
		},
		{
			name: "invalid owner empty string",
			variables: map[string]any{
				"owner": "",
			},
			wantErr: true,
		},
		{
			name: "invalid owner too long",
			variables: map[string]any{
				"owner": strings.Repeat("a", 101),
			},
			wantErr: true,
		},
		{
			name: "negative number",
			variables: map[string]any{
				"count": -1,
			},
			wantErr: true,
		},
		{
			name: "number too large",
			variables: map[string]any{
				"count": 1000001,
			},
			wantErr: true,
		},
		{
			name: "valid multiple variables",
			variables: map[string]any{
				"owner":  "test",
				"repo":   "repo",
				"number": 123,
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateGraphQLVariables(tt.variables)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateGraphQLVariables() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestExtractGraphQLQueryType(t *testing.T) {
	tests := []struct {
		name  string
		query string
		want  string
	}{
		{
			name: "user repositories query",
			query: `query($login: String!) {
				user(login: $login) {
					name
				}
			}`,
			want: "user-repositories",
		},
		{
			name: "organization repositories query",
			query: `query($login: String!) {
				organization(login: $login) {
					name
				}
			}`,
			want: "organization-repositories",
		},
		{
			name: "repository query without pullRequests",
			query: `query($owner: String!, $repo: String!) {
				repository(owner: $owner, name: $repo) {
					name
				}
			}`,
			want: "repository-query",
		},
		{
			name:  "empty query",
			query: "",
			want:  "unknown-graphql",
		},
		{
			name:  "whitespace only",
			query: "   \n\t  ",
			want:  "unknown-graphql",
		},
		{
			name: "simple query without parameters",
			query: `{
				viewer {
					login
				}
			}`,
			want: "unknown-graphql",
		},
		{
			name: "repository with pullRequests",
			query: `query($owner: String!, $repo: String!) {
				repository(owner: $owner, name: $repo) {
					pullRequests {
						nodes {
							number
						}
					}
				}
			}`,
			want: "repository-pullrequests",
		},
		{
			name: "repository commit history",
			query: `query($owner: String!, $repo: String!) {
				repository(owner: $owner, name: $repo) {
					defaultBranchRef {
						target {
							history {
								nodes {
									oid
								}
							}
						}
					}
				}
			}`,
			want: "repository-query", // Returns repository-query because no pullRequests field
		},
		{
			name: "org batch PRs (no query prefix)",
			query: `{
				organization {
					repositories {
						nodes {
							name
						}
					}
				}
			}`,
			want: "org-batch-prs",
		},
		{
			name: "repo recent PRs (no query prefix)",
			query: `{
				repository {
					pullRequests {
						nodes {
							number
						}
					}
				}
			}`,
			want: "repo-recent-prs",
		},
		{
			name: "commit history (no query prefix)",
			query: `{
				history {
					nodes {
						oid
					}
				}
			}`,
			want: "commit-history",
		},
		{
			name: "repository with history and pullRequests",
			query: `query($owner: String!, $repo: String!) {
				repository(owner: $owner, name: $repo) {
					history {
						nodes {
							oid
						}
					}
					pullRequests {
						nodes {
							number
						}
					}
				}
			}`,
			want: "repository-commit-history",
		},
		{
			name: "query with field containing parentheses",
			query: `query($login: String!) {
				customField(param: "value") {
					data
				}
			}`,
			want: "customField",
		},
		{
			name: "query with field containing space",
			query: `query($id: ID!) {
				node id {
					... on User {
						login
					}
				}
			}`,
			want: "node",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractGraphQLQueryType(tt.query)
			if got != tt.want {
				t.Errorf("extractGraphQLQueryType() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestClient_MakeGraphQLRequest_ValidateVariables(t *testing.T) {
	c := &Client{
		httpClient: &http.Client{},
		token:      "test-token",
	}

	ctx := context.Background()

	// Test with invalid variables (path traversal attempt)
	invalidVars := map[string]any{
		"owner": "../etc/passwd",
	}

	_, err := c.MakeGraphQLRequest(ctx, "query { test }", invalidVars)
	if err == nil {
		t.Error("expected error for invalid variables")
	}

	if err != nil && !strings.Contains(err.Error(), "invalid GraphQL variables") {
		t.Errorf("expected error to contain 'invalid GraphQL variables', got %q", err.Error())
	}
}

func TestValidateGraphQLVariables_EdgeCases(t *testing.T) {
	tests := []struct {
		name      string
		variables map[string]any
		wantErr   bool
	}{
		{
			name: "nil value",
			variables: map[string]any{
				"value": nil,
			},
			wantErr: false,
		},
		{
			name: "empty string",
			variables: map[string]any{
				"value": "",
			},
			wantErr: false,
		},
		{
			name: "zero int",
			variables: map[string]any{
				"value": 0,
			},
			wantErr: false,
		},
		{
			name: "empty slice",
			variables: map[string]any{
				"value": []string{},
			},
			wantErr: false,
		},
		{
			name: "empty map",
			variables: map[string]any{
				"value": map[string]any{},
			},
			wantErr: false,
		},
		{
			name: "complex nested structure",
			variables: map[string]any{
				"input": map[string]any{
					"user": map[string]any{
						"name": "test",
						"tags": []string{"a", "b"},
					},
					"meta": map[string]any{
						"count": 5,
					},
				},
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateGraphQLVariables(tt.variables)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateGraphQLVariables() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestGraphQLConstants(t *testing.T) {
	// Test that constants are defined with reasonable values
	if maxQuerySize <= 0 {
		t.Error("maxQuerySize should be positive")
	}

	if maxGraphQLVarLength <= 0 {
		t.Error("maxGraphQLVarLength should be positive")
	}

	if maxGraphQLVarNum <= 0 {
		t.Error("maxGraphQLVarNum should be positive")
	}

	if maxGitHubNameLength != 100 {
		t.Errorf("expected maxGitHubNameLength to be 100, got %d", maxGitHubNameLength)
	}

	if graphQLEndpoint != "https://api.github.com/graphql" {
		t.Errorf("expected graphQLEndpoint to be https://api.github.com/graphql, got %s", graphQLEndpoint)
	}
}

func TestClient_MakeGraphQLRequest_QueryTooLarge(t *testing.T) {
	c := &Client{
		httpClient: &http.Client{},
		token:      "test-token",
	}

	ctx := context.Background()

	// Create a query that's too large (> maxQuerySize)
	largeQuery := strings.Repeat("a", maxQuerySize+1)

	_, err := c.MakeGraphQLRequest(ctx, largeQuery, nil)
	if err == nil {
		t.Error("expected error for query too large")
	}

	if !strings.Contains(err.Error(), "GraphQL query too large") {
		t.Errorf("expected error to contain 'GraphQL query too large', got %q", err.Error())
	}
}
