package main

import "sort"

// frequencyCounter counts occurrences and returns results.
type frequencyCounter map[string]int

// add increments the count for a value.
func (fc frequencyCounter) add(value string) {
	if value != "" {
		fc[value]++
	}
}

// addAll adds multiple values.
func (fc frequencyCounter) addAll(values []string) {
	for _, v := range values {
		fc.add(v)
	}
}

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

// add adds a score to a value.
func (sa scoreAggregator) add(value string, score float64) {
	if value != "" && score > 0 {
		sa[value] += score
	}
}

// best returns the value with highest score.
func (sa scoreAggregator) best() (string, float64) {
	var result string
	var maxScore float64
	for val, score := range sa {
		if score > maxScore {
			maxScore = score
			result = val
		}
	}
	return result, maxScore
}

// abs returns the absolute value of an integer.
func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}