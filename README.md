# Better Reviewers

A Go program that intelligently finds and assigns reviewers for GitHub pull requests based on code context and reviewer activity.

## Features

- **Context-based reviewer selection**: Finds reviewers who have previously worked on the lines being changed
- **Activity-based reviewer selection**: Identifies reviewers who have been active on similar files
- **Configurable time constraints**: Only processes PRs within specified age ranges
- **Dry-run mode**: Test the logic without actually assigning reviewers
- **Polling support**: Continuously monitor repositories for new PRs
- **Comprehensive logging**: Detailed logs to understand reviewer selection decisions

## Installation

```bash
go build -o better-reviewers
```

## Prerequisites

- Go 1.21 or later
- GitHub CLI (`gh`) installed and authenticated
- GitHub token with appropriate permissions (repo access)

## Usage

### Single PR Analysis

```bash
./better-reviewers -pr "https://github.com/owner/repo/pull/123"
./better-reviewers -pr "owner/repo#123"
```

### Project Monitoring

```bash
./better-reviewers -project "owner/repo"
```

### Organization Monitoring (Coming Soon)

```bash
./better-reviewers -org "myorg"
```

### Polling Mode

```bash
./better-reviewers -project "owner/repo" -poll 1h
```

### Dry Run Mode

```bash
./better-reviewers -pr "owner/repo#123" -dry-run
```

## Command Line Options

### Target Flags (Mutually Exclusive)

- `-pr`: Pull request URL or shorthand (e.g., `https://github.com/owner/repo/pull/123` or `owner/repo#123`)
- `-project`: GitHub project to monitor (e.g., `owner/repo`)
- `-org`: GitHub organization to monitor (not yet implemented)

### Behavior Flags

- `-poll`: Polling interval (e.g., `1h`, `30m`). If not set, runs once
- `-dry-run`: Run in dry-run mode (no actual reviewer assignments)
- `-min-age`: Minimum time since last commit or review for PR assignment (default: 1h)
- `-max-age`: Maximum time since last commit or review for PR assignment (default: 180 days)

## How It Works

### Reviewer Selection Algorithm

The program finds exactly two reviewers for each PR: a **primary reviewer** (with author context) and a **secondary reviewer** (who actively reviews code).

#### Primary Reviewer (Author Context)

The primary reviewer is selected based on who knows the code best, in this priority order:

1. **Blame-based Authors**:
   - Examines GitHub blame history for changed files
   - Considers the top 5 PRs by overlap with edited lines
   - Selects authors of these PRs who still have write access
   - Verifies write access via author_association

2. **Directory Author** (fallback):
   - Most recent author of a merged PR in the same directory

3. **Project Author** (fallback):
   - Most recent author of a merged PR in the project

#### Secondary Reviewer (Active Reviewer)

The secondary reviewer is selected based on review activity, in this priority order:

1. **Blame-based Reviewers**:
   - Examines the same GitHub blame history
   - Considers the top 5 PRs by overlap with edited lines
   - Selects reviewers/approvers of these PRs

2. **Directory Reviewer** (fallback):
   - Most recent reviewer of a merged PR in the same directory

3. **Project Reviewer** (fallback):
   - Most recent reviewer of a merged PR in the project

### Assignment Rules

- Always attempts to assign exactly 2 reviewers: primary and secondary
- The PR author cannot be a reviewer
- Primary and secondary must be different people
- Each reviewer selection logs the mechanism used (e.g., "primary-blame-author", "secondary-directory-reviewer")
- **Draft PRs**: Skips reviewer assignment for draft PRs but logs who would have been assigned

### Error Handling

- If after exhausting all fallback mechanisms, the only candidate found is the PR author, the program will error with a clear message
- This ensures that the PR author is never assigned as their own reviewer
- The error message will indicate that all candidates have been exhausted

### GitHub API Usage

The program uses:
- GitHub REST API v3 for PR data, file changes, and reviews
- GitHub GraphQL API v4 for blame data and efficient directory/project searches
- **No longer uses the slow Search API** - replaced with GraphQL queries
- Caches blame data to avoid redundant API calls
- 120-second timeout for API calls to handle slow responses
- Retry logic with exponential backoff for GraphQL requests (up to 3 attempts)
- Proper authentication using `gh auth token` for all API calls

## Architecture

The codebase is organized into several key files:

- `main.go`: Command-line interface and main program logic
- `github.go`: GitHub API client implementation
- `reviewer.go`: Core reviewer finding and assignment logic
- `analysis.go`: Blame data analysis and PR relationship detection
- `main_test.go`: Unit tests for core functionality

## Testing

Run the test suite:

```bash
go test -v
```

## Example Output

### Successful Primary/Secondary Selection
```
2024/01/15 10:30:45 Processing PR owner/repo#123: Add new feature
2024/01/15 10:30:45 Analyzing 3 changed files for PR 123
2024/01/15 10:30:46 === Finding PRIMARY reviewer (author context) ===
2024/01/15 10:30:46 Checking blame-based authors for primary reviewer
2024/01/15 10:30:46 Found author alice from PR 89 with 25 line overlap
2024/01/15 10:30:46 Found author charlie from PR 76 with 15 line overlap
2024/01/15 10:30:46 Selected blame-based author: alice (score: 25, association: MEMBER)
2024/01/15 10:30:47 === Finding SECONDARY reviewer (active reviewer) ===
2024/01/15 10:30:47 Checking blame-based reviewers for secondary reviewer
2024/01/15 10:30:47 Found reviewer bob from PR 89 with 25 line overlap
2024/01/15 10:30:47 Found reviewer dave from PR 76 with 15 line overlap
2024/01/15 10:30:47 Selected blame-based reviewer: bob (score: 25)
2024/01/15 10:30:47 PRIMARY reviewer selected: alice (method: primary-blame-author)
2024/01/15 10:30:47 SECONDARY reviewer selected: bob (method: secondary-blame-reviewer)
2024/01/15 10:30:47 Found 2 reviewer candidates for PR 123
2024/01/15 10:30:47 Adding reviewers [alice bob] to PR owner/repo#123
2024/01/15 10:30:48 Successfully added reviewers [alice bob] to PR 123
```

### Fallback Mechanism in Action
```
2024/01/15 10:31:00 Processing PR owner/repo#124: Fix minor bug
2024/01/15 10:31:00 Analyzing 3 changed files for PR 124
2024/01/15 10:31:01 === Finding PRIMARY reviewer (author context) ===
2024/01/15 10:31:01 Checking blame-based authors for primary reviewer
2024/01/15 10:31:01 No blame-based authors found, checking directory authors
2024/01/15 10:31:02 PRIMARY reviewer selected: charlie (method: primary-directory-author)
2024/01/15 10:31:02 === Finding SECONDARY reviewer (active reviewer) ===
2024/01/15 10:31:02 Checking blame-based reviewers for secondary reviewer
2024/01/15 10:31:02 No blame-based reviewers found, checking directory reviewers
2024/01/15 10:31:03 No directory reviewers found, checking project reviewers
2024/01/15 10:31:03 SECONDARY reviewer selected: dave (method: secondary-project-reviewer)
2024/01/15 10:31:03 Found 2 reviewer candidates for PR 124
2024/01/15 10:31:03 Adding reviewers [charlie dave] to PR owner/repo#124
2024/01/15 10:31:03 Successfully added reviewers [charlie dave] to PR 124
```

### Error Case: Only PR Author Found
```
2024/01/15 10:32:00 Processing PR owner/repo#125: Initial commit
2024/01/15 10:32:00 Top changed files for PR 125: [main.go, go.mod, README.md]
2024/01/15 10:32:01 Finding context reviewers using blame data for 3 files
2024/01/15 10:32:01 Finding activity reviewers for 3 files
2024/01/15 10:32:01 Found only 0 candidates, trying fallback to line authors
2024/01/15 10:32:01 Finding line authors with write access for fallback
2024/01/15 10:32:01 Line author fallback candidate: john-doe (lines: 50, association: OWNER, method: fallback-line-author)
2024/01/15 10:32:02 Found only 1 candidates, trying fallback to recent file authors
2024/01/15 10:32:02 Found only 1 candidates, trying directory-based fallbacks
2024/01/15 10:32:02 Found only 1 candidates, trying project-wide fallbacks
2024/01/15 10:32:03 Project author fallback candidate: john-doe (method: fallback-project-author)
2024/01/15 10:32:03 Failed to find reviewer candidates: exhausted all reviewer candidates: the only candidate found was the PR author (john-doe)
```

### Draft PR Handling
```
2024/01/15 10:33:00 Processing PR owner/repo#126 [DRAFT]: WIP: New feature
2024/01/15 10:33:00 Top changed files for PR 126: [feature.go, feature_test.go]
2024/01/15 10:33:01 Finding context reviewers using blame data for 2 files
2024/01/15 10:33:01 Context reviewer candidate: alice (score: 20, method: context-blame-approver)
2024/01/15 10:33:01 Activity reviewer candidate: bob (PR size: 120, method: activity-recent-approver)
2024/01/15 10:33:01 Found 2 reviewer candidates for PR 126
2024/01/15 10:33:01 Selected reviewer: alice (method: context-blame-approver, context score: 20, activity score: 0)
2024/01/15 10:33:01 Selected reviewer: bob (method: activity-recent-approver, context score: 0, activity score: 120)
2024/01/15 10:33:01 PR 126 is a draft - skipping reviewer assignment
2024/01/15 10:33:01 Would have assigned reviewers [alice bob] to PR 126 if it wasn't a draft
```

## Contributing

1. Follow Go best practices and code review guidelines from go.dev
2. Add tests for new functionality
3. Ensure comprehensive logging for debugging
4. Use minimal external dependencies

## License

This project is provided as-is for educational and productivity purposes.