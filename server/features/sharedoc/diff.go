package sharedoc

import (
	"regexp"
)

// DiffOp represents a diff operation type.
type DiffOp int

const (
	DiffEqual DiffOp = iota
	DiffDelete
	DiffInsert
)

// DiffChunk is a contiguous run of tokens with the same operation.
type DiffChunk struct {
	Type DiffOp `json:"type"`
	Text string `json:"text"`
}

var tokenRe = regexp.MustCompile(`\S+|\s+`)

// WordDiff computes a word-level diff between oldText and newText.
// It returns a slice of chunks tagged as equal, delete or insert.
func WordDiff(oldText, newText string) []DiffChunk {
	oldTokens := tokenRe.FindAllString(oldText, -1)
	newTokens := tokenRe.FindAllString(newText, -1)
	if oldTokens == nil {
		oldTokens = []string{}
	}
	if newTokens == nil {
		newTokens = []string{}
	}

	m, n := len(oldTokens), len(newTokens)
	// LCS length matrix
	lcs := make([][]int, m+1)
	for i := range lcs {
		lcs[i] = make([]int, n+1)
	}
	for i := m - 1; i >= 0; i-- {
		for j := n - 1; j >= 0; j-- {
			if oldTokens[i] == newTokens[j] {
				lcs[i][j] = 1 + lcs[i+1][j+1]
			} else if lcs[i+1][j] > lcs[i][j+1] {
				lcs[i][j] = lcs[i+1][j]
			} else {
				lcs[i][j] = lcs[i][j+1]
			}
		}
	}

	var chunks []DiffChunk
	i, j := 0, 0
	for i < m || j < n {
		if i < m && j < n && oldTokens[i] == newTokens[j] {
			// equal token
			chunks = appendChunk(chunks, DiffEqual, oldTokens[i])
			i++
			j++
		} else if j < n && (i == m || lcs[i][j+1] >= lcs[i+1][j]) {
			// inserted token from new
			chunks = appendChunk(chunks, DiffInsert, newTokens[j])
			j++
		} else if i < m {
			// deleted token from old
			chunks = appendChunk(chunks, DiffDelete, oldTokens[i])
			i++
		} else {
			// should not happen
			break
		}
	}
	return chunks
}

func appendChunk(chunks []DiffChunk, op DiffOp, text string) []DiffChunk {
	if len(chunks) > 0 && chunks[len(chunks)-1].Type == op {
		chunks[len(chunks)-1].Text += text
		return chunks
	}
	return append(chunks, DiffChunk{Type: op, Text: text})
}
