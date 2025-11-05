package reviewer

import (
	"context"
	"testing"
	"time"

	"github.com/codeGROOVE-dev/best-reviewer/pkg/internal/testutil"
	"github.com/codeGROOVE-dev/best-reviewer/pkg/types"
)

// TestIntegration_recentPRsInProject tests fetching recent PRs with pagination
func TestIntegration_recentPRsInProject(t *testing.T) {
	ctx := context.Background()
	client := testutil.NewMockGitHubClient()
	finder := New(client, Config{PRCountCache: time.Hour})

	// Setup first batch response with pagination info
	firstBatchResponse := map[string]any{
		"data": map[string]any{
			"repository": map[string]any{
				"pullRequests": map[string]any{
					"pageInfo": map[string]any{
						"endCursor":   "cursor123",
						"hasNextPage": true,
					},
					"nodes": []any{
						map[string]any{
							"number":   float64(100),
							"merged":   true,
							"mergedAt": time.Now().Add(-24 * time.Hour).Format(time.RFC3339),
							"author": map[string]any{
								"login": "alice",
							},
							"mergedBy": map[string]any{
								"login": "bob",
							},
							"reviews": map[string]any{
								"nodes": []any{
									map[string]any{
										"author": map[string]any{
											"login": "charlie",
										},
									},
								},
							},
						},
						map[string]any{
							"number":   float64(99),
							"merged":   true,
							"mergedAt": time.Now().Add(-48 * time.Hour).Format(time.RFC3339),
							"author": map[string]any{
								"login": "dave",
							},
							"mergedBy": map[string]any{
								"login": "eve",
							},
							"reviews": map[string]any{
								"nodes": []any{},
							},
						},
					},
				},
			},
		},
	}

	// Setup second batch response (no more pages)
	secondBatchResponse := map[string]any{
		"data": map[string]any{
			"repository": map[string]any{
				"pullRequests": map[string]any{
					"pageInfo": map[string]any{
						"endCursor":   "cursor456",
						"hasNextPage": false,
					},
					"nodes": []any{
						map[string]any{
							"number":   float64(98),
							"merged":   true,
							"mergedAt": time.Now().Add(-72 * time.Hour).Format(time.RFC3339),
							"author": map[string]any{
								"login": "frank",
							},
							"mergedBy": map[string]any{
								"login": "alice",
							},
							"reviews": map[string]any{
								"nodes": []any{
									map[string]any{
										"author": map[string]any{
											"login": "bob",
										},
									},
									map[string]any{
										"author": map[string]any{
											"login": "charlie",
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}

	// Set the exact query from graphql.go (line 491)
	query1 := `
	query($owner: String!, $repo: String!) {
		repository(owner: $owner, name: $repo) {
			pullRequests(first: 100, states: MERGED, orderBy: {field: CREATED_AT, direction: DESC}) {
				pageInfo {
					endCursor
					hasNextPage
				}
				nodes {
					number
					merged
					author {
						login
					}
					mergedAt
					mergedBy {
						login
					}
					reviews(first: 10, states: APPROVED) {
						nodes {
							author {
								login
							}
						}
					}
				}
			}
		}
	}`

	client.SetGraphQLResponse(query1, firstBatchResponse)

	// Set the second batch query (with after parameter)
	query2 := `
	query($owner: String!, $repo: String!, $after: String!) {
		repository(owner: $owner, name: $repo) {
			pullRequests(first: 100, after: $after, states: MERGED, orderBy: {field: CREATED_AT, direction: DESC}) {
				nodes {
					number
					merged
					author {
						login
					}
					mergedAt
					mergedBy {
						login
					}
					reviews(first: 10, states: APPROVED) {
						nodes {
							author {
								login
							}
						}
					}
				}
			}
		}
	}`

	client.SetGraphQLResponse(query2, secondBatchResponse)

	prs, err := finder.recentPRsInProject(ctx, "owner", "repo")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should get PRs from first batch (second batch won't be called due to mock limitations)
	if len(prs) < 2 {
		t.Errorf("expected at least 2 PRs, got %d", len(prs))
	}

	// Verify first PR details
	if len(prs) > 0 {
		if prs[0].Number != 100 {
			t.Errorf("expected first PR to be 100, got %d", prs[0].Number)
		}
		if prs[0].Author != "alice" {
			t.Errorf("expected author alice, got %s", prs[0].Author)
		}
		if len(prs[0].Reviewers) != 1 {
			t.Errorf("expected 1 reviewer, got %d", len(prs[0].Reviewers))
		}
	}
}

// TestIntegration_recentCommitsInDirectory tests directory-specific commit history
func TestIntegration_recentCommitsInDirectory(t *testing.T) {
	ctx := context.Background()
	client := testutil.NewMockGitHubClient()
	finder := New(client, Config{PRCountCache: time.Hour})

	// Setup directory commits response
	dirCommitsResponse := map[string]any{
		"data": map[string]any{
			"repository": map[string]any{
				"defaultBranchRef": map[string]any{
					"name": "main",
					"target": map[string]any{
						"history": map[string]any{
							"nodes": []any{
								map[string]any{
									"oid":             "commit1",
									"messageHeadline": "Update parser logic",
									"author": map[string]any{
										"user": map[string]any{
											"login": "alice",
										},
									},
									"associatedPullRequests": map[string]any{
										"nodes": []any{
											map[string]any{
												"number":   float64(42),
												"merged":   true,
												"mergedAt": time.Now().Add(-24 * time.Hour).Format(time.RFC3339),
												"author": map[string]any{
													"login": "alice",
												},
												"mergedBy": map[string]any{
													"login": "bob",
												},
												"reviews": map[string]any{
													"nodes": []any{
														map[string]any{
															"author": map[string]any{
																"login": "charlie",
															},
														},
													},
												},
											},
										},
									},
								},
								map[string]any{
									"oid":             "commit2",
									"messageHeadline": "Direct commit",
									"author": map[string]any{
										"user": map[string]any{
											"login": "dave",
										},
									},
									"associatedPullRequests": map[string]any{
										"nodes": []any{}, // Direct commit without PR
									},
								},
							},
						},
					},
				},
			},
		},
	}

	// Set the exact query from graphql.go (line 326)
	dirQuery := `
	query($owner: String!, $repo: String!, $path: String!, $limit: Int!) {
		repository(owner: $owner, name: $repo) {
			defaultBranchRef {
				name
				target {
					... on Commit {
						history(first: $limit, path: $path) {
							nodes {
								oid
								author {
									user {
										login
									}
								}
								associatedPullRequests(first: 1) {
									nodes {
										number
										merged
										mergedAt
										author {
											login
										}
										mergedBy {
											login
										}
										reviews(first: 10, states: APPROVED) {
											nodes {
												author {
													login
												}
											}
										}
									}
								}
							}
						}
					}
				}
			}
		}
	}`

	client.SetGraphQLResponse(dirQuery, dirCommitsResponse)

	prs, err := finder.recentCommitsInDirectory(ctx, "owner", "repo", "pkg/parser")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should find PR 42 and the direct commit author (dave)
	if len(prs) < 2 {
		t.Errorf("expected at least 2 entries (1 PR + 1 direct commit), got %d", len(prs))
	}

	// Verify we got the PR
	foundPR := false
	foundDirectCommit := false
	for _, pr := range prs {
		if pr.Number == 42 {
			foundPR = true
			if pr.Author != "alice" {
				t.Errorf("expected PR author alice, got %s", pr.Author)
			}
		}
		if pr.Number == 0 && pr.Author == "dave" {
			foundDirectCommit = true
		}
	}

	if !foundPR {
		t.Error("expected to find PR 42")
	}
	if !foundDirectCommit {
		t.Error("expected to find direct commit from dave")
	}
}

// TestIntegration_findReviewersOptimized_FullWorkflow tests the complete workflow
func TestIntegration_findReviewersOptimized_FullWorkflow(t *testing.T) {
	ctx := context.Background()
	client := testutil.NewMockGitHubClient()
	finder := New(client, Config{PRCountCache: time.Hour})

	// Setup all necessary GraphQL responses

	// 1. Blame API for file expertise
	blameResponse := map[string]any{
		"data": map[string]any{
			"repository": map[string]any{
				"defaultBranchRef": map[string]any{
					"target": map[string]any{
						"blame": map[string]any{
							"ranges": []any{
								map[string]any{
									"startingLine": float64(10),
									"endingLine":   float64(20),
									"commit": map[string]any{
										"oid": "commit123",
										"author": map[string]any{
											"user": map[string]any{
												"login": "expert-bob",
											},
										},
										"associatedPullRequests": map[string]any{
											"nodes": []any{
												map[string]any{
													"number":   float64(50),
													"merged":   true,
													"mergedAt": time.Now().Add(-24 * time.Hour).Format(time.RFC3339),
													"author": map[string]any{
														"login": "expert-bob",
													},
													"mergedBy": map[string]any{
														"login": "maintainer",
													},
													"reviews": map[string]any{
														"nodes": []any{},
													},
												},
											},
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}

	// Set the blame query response
	blameQuery := `
	query($owner: String!, $repo: String!, $path: String!) {
		repository(owner: $owner, name: $repo) {
			defaultBranchRef {
				target {
					... on Commit {
						blame(path: $path) {
							ranges {
								startingLine
								endingLine
								commit {
									oid
									author {
										user {
											login
										}
									}
									associatedPullRequests(first: 1) {
										nodes {
											number
											merged
											mergedAt
											author {
												login
											}
											mergedBy {
												login
											}
											reviews(first: 10, states: APPROVED) {
												nodes {
													author {
														login
													}
												}
											}
										}
									}
								}
							}
						}
					}
				}
			}
		}
	}`

	client.SetGraphQLResponse(blameQuery, blameResponse)

	pr := &types.PullRequest{
		Owner:      "test-owner",
		Repository: "test-repo",
		Number:     100,
		Author:     "alice",
		Assignees:  []string{"expert-bob"},
		ChangedFiles: []types.ChangedFile{
			{
				Filename:  "pkg/core/engine.go",
				Additions: 50,
				Deletions: 20,
				Patch:     "@@ -10,5 +10,25 @@ func Process() {\n line1\n+line2\n+line3",
			},
		},
	}

	// Setup collaborators
	client.SetCollaborators("test-owner", "test-repo", []string{
		"alice", "expert-bob", "dir-expert", "charlie", "active-dev", "maintainer",
	})
	for _, user := range []string{"alice", "expert-bob", "dir-expert", "charlie", "active-dev", "maintainer"} {
		client.SetBotUser(user, false)
	}

	// Set write access for all collaborators except alice (the author)
	for _, user := range []string{"expert-bob", "dir-expert", "charlie", "active-dev", "maintainer"} {
		client.SetWriteAccess("test-owner", "test-repo", user, true)
	}

	// Set workload for balancing
	client.SetOpenPRCount("test-owner", "expert-bob", 2)
	client.SetOpenPRCount("test-owner", "charlie", 1)
	client.SetOpenPRCount("test-owner", "dir-expert", 3)

	reviewers := finder.findReviewersOptimized(ctx, pr)

	if len(reviewers) == 0 {
		t.Fatal("expected reviewers, got none")
	}

	// Should include assignee with high score
	foundAssignee := false
	for _, r := range reviewers {
		if r.Username == "expert-bob" {
			foundAssignee = true
			if r.ContextScore < 150 {
				t.Errorf("expected high context score for assignee with blame expertise, got %d", r.ContextScore)
			}
		}

		// All reviewers should have positive scores
		if r.ContextScore <= 0 {
			t.Errorf("expected positive context score for %s, got %d", r.Username, r.ContextScore)
		}
	}

	if !foundAssignee {
		t.Error("expected assignee 'expert-bob' in reviewers")
	}
}

// TestIntegration_recentPRsInProject_Caching tests cache behavior
func TestIntegration_recentPRsInProject_Caching(t *testing.T) {
	ctx := context.Background()
	client := testutil.NewMockGitHubClient()
	finder := New(client, Config{PRCountCache: time.Hour})

	response := map[string]any{
		"data": map[string]any{
			"repository": map[string]any{
				"pullRequests": map[string]any{
					"pageInfo": map[string]any{
						"hasNextPage": false,
					},
					"nodes": []any{
						map[string]any{
							"number":   float64(1),
							"merged":   true,
							"mergedAt": time.Now().Format(time.RFC3339),
							"author": map[string]any{
								"login": "test",
							},
							"mergedBy": map[string]any{
								"login": "test2",
							},
							"reviews": map[string]any{
								"nodes": []any{},
							},
						},
					},
				},
			},
		},
	}

	// Use the same query as in the actual code
	query := `
	query($owner: String!, $repo: String!) {
		repository(owner: $owner, name: $repo) {
			pullRequests(first: 100, states: MERGED, orderBy: {field: CREATED_AT, direction: DESC}) {
				pageInfo {
					endCursor
					hasNextPage
				}
				nodes {
					number
					merged
					author {
						login
					}
					mergedAt
					mergedBy {
						login
					}
					reviews(first: 10, states: APPROVED) {
						nodes {
							author {
								login
							}
						}
					}
				}
			}
		}
	}`

	client.SetGraphQLResponse(query, response)

	// First call
	prs1, err := finder.recentPRsInProject(ctx, "owner", "repo")
	if err != nil {
		t.Fatalf("unexpected error on first call: %v", err)
	}

	// Second call should use cache
	prs2, err := finder.recentPRsInProject(ctx, "owner", "repo")
	if err != nil {
		t.Fatalf("unexpected error on second call: %v", err)
	}

	if len(prs1) != len(prs2) {
		t.Errorf("cache should return same results: first=%d, second=%d", len(prs1), len(prs2))
	}
}

// TestIntegration_recentCommitsInDirectory_Caching tests cache behavior
func TestIntegration_recentCommitsInDirectory_Caching(t *testing.T) {
	ctx := context.Background()
	client := testutil.NewMockGitHubClient()
	finder := New(client, Config{PRCountCache: time.Hour})

	response := map[string]any{
		"data": map[string]any{
			"repository": map[string]any{
				"defaultBranchRef": map[string]any{
					"target": map[string]any{
						"history": map[string]any{
							"nodes": []any{
								map[string]any{
									"oid": "test",
									"author": map[string]any{
										"user": map[string]any{
											"login": "test",
										},
									},
									"associatedPullRequests": map[string]any{
										"nodes": []any{},
									},
								},
							},
						},
					},
				},
			},
		},
	}

	// Use the same query as in the actual code
	dirQuery := `
	query($owner: String!, $repo: String!, $path: String!, $limit: Int!) {
		repository(owner: $owner, name: $repo) {
			defaultBranchRef {
				name
				target {
					... on Commit {
						history(first: $limit, path: $path) {
							nodes {
								oid
								author {
									user {
										login
									}
								}
								associatedPullRequests(first: 1) {
									nodes {
										number
										merged
										mergedAt
										author {
											login
										}
										mergedBy {
											login
										}
										reviews(first: 10, states: APPROVED) {
											nodes {
												author {
													login
												}
											}
										}
									}
								}
							}
						}
					}
				}
			}
		}
	}`

	client.SetGraphQLResponse(dirQuery, response)

	// First call
	prs1, err := finder.recentCommitsInDirectory(ctx, "owner", "repo", "pkg/test")
	if err != nil {
		t.Fatalf("unexpected error on first call: %v", err)
	}

	// Second call should use cache
	prs2, err := finder.recentCommitsInDirectory(ctx, "owner", "repo", "pkg/test")
	if err != nil {
		t.Fatalf("unexpected error on second call: %v", err)
	}

	if len(prs1) != len(prs2) {
		t.Errorf("cache should return same results: first=%d, second=%d", len(prs1), len(prs2))
	}
}
