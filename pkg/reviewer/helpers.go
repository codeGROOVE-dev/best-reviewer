package reviewer

import (
	"fmt"
	"sort"
	"strings"
)

// frequencyCounter counts occurrences and returns results.
type frequencyCounter map[string]int

// best returns the value with highest count.
func (fc frequencyCounter) best() string {
	var result string
	maxCount := 0
	for val, count := range fc {
		if count > maxCount {
			maxCount = count
			result = val
		}
	}
	return result
}

// top returns the top N values by count.
func (fc frequencyCounter) top(n int) []string {
	type pair struct {
		value string
		count int
	}

	pairs := make([]pair, 0, len(fc))
	for val, count := range fc {
		pairs = append(pairs, pair{val, count})
	}

	sort.Slice(pairs, func(i, j int) bool {
		return pairs[i].count > pairs[j].count
	})

	result := make([]string, 0, n)
	for i, p := range pairs {
		if i >= n {
			break
		}
		result = append(result, p.value)
	}
	return result
}

// scoreAggregator accumulates scores for values.
type scoreAggregator map[string]float64

// best returns the value with highest score.
func (sa scoreAggregator) best() string {
	var bestValue string
	var bestScore float64
	for val, score := range sa {
		if score > bestScore {
			bestScore = score
			bestValue = val
		}
	}
	return bestValue
}

// makeCacheKey creates a cache key from components.
func makeCacheKey(parts ...any) string {
	strParts := make([]string, len(parts))
	for i, part := range parts {
		strParts[i] = fmt.Sprint(part)
	}
	return strings.Join(strParts, ":")
}

// directories extracts unique directories from file paths.
func directories(files []string) []string {
	dirMap := make(map[string]bool)
	for _, file := range files {
		parts := strings.Split(file, "/")
		for i := 1; i <= len(parts)-1; i++ {
			dir := strings.Join(parts[:i], "/")
			dirMap[dir] = true
		}
	}

	dirs := make([]string, 0, len(dirMap))
	for dir := range dirMap {
		dirs = append(dirs, dir)
	}

	// Sort by depth (deeper first), then alphabetically
	sort.Slice(dirs, func(i, j int) bool {
		depthI := strings.Count(dirs[i], "/")
		depthJ := strings.Count(dirs[j], "/")
		if depthI != depthJ {
			return depthI > depthJ
		}
		return dirs[i] < dirs[j]
	})

	return dirs
}
